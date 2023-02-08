package dtcp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/akmistry/go-util/ringbuffer"
)

type tcpConnState int

const (
	// TCP connection state machine (RFC 9293 3.3.2 (mostly))
	tcpConnStateInit = iota
	tcpConnStateSynRecv
	tcpConnStateEst
	tcpConnStateFinWait1
	tcpConnStateFinWait2
	// Received FIN from remote end, waiting for close from the user.
	tcpConnStateCloseWait
	tcpConnStateClosing
	// Received and sent FIN and waiting on FIN-ACK from remote.
	tcpConnStateLastAck
	tcpConnStateTimeWait
	// Any connection in this state should be removed.
	tcpConnStateClosed
)

const (
	// Initial window size
	// TODO: Increase this
	tcpInitialWindowSize = 4096

	// Mask of high-order 4-byte of the tracked 64-bit sequence numbers
	tcpSeqHighMask = 0xFFFFFFFF00000000

	// Minimum size of a TCP packet (minimum header size)
	tcpMinPacketSize = TcpMinHeaderSize

	// Use a static 1 second retransmission timeout
	// TODO: Replace with Karn's algorithm, as required by RFC 9293 3.8.1
	tcpRto = time.Second

	gigabyte = (1 << 30)
)

type TCPConnState struct {
	sender                TCPSender
	localPort, remotePort uint16
	remoteAddr            *net.IPAddr

	state tcpConnState

	// TODO: Support SACK.
	// Send state
	sendBuf    *SyncedBuffer
	sendClosed bool

	// Receive state
	recvBuf        *ringbuffer.RingBuffer
	recvWindowSize int
	recvStart      uint64
	recvClosed     bool
	pendingAck     bool

	lastAckTime time.Time

	lock sync.Mutex
	cond *sync.Cond
}

func NewTCPConnState(tcpSynHeader *TCPHeader, sender TCPSender, remoteAddr *net.IPAddr) *TCPConnState {
	s := &TCPConnState{
		sender:     sender,
		localPort:  tcpSynHeader.DstPort,
		remotePort: tcpSynHeader.SrcPort,
		remoteAddr: remoteAddr,
		sendBuf:    NewSyncedBuffer(1, tcpInitialWindowSize),
		// TODO: Use a SYN cookie.
		state: tcpConnStateInit,
	}
	s.cond = sync.NewCond(&s.lock)
	go s.retransmitTimeout()
	return s
}

func (s *TCPConnState) retransmitTimeout() {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.state != tcpConnStateClosed {
		s.lock.Unlock()
		time.Sleep(tcpRto)
		now := time.Now()
		s.lock.Lock()

		if now.Sub(s.lastAckTime) > tcpRto {
			LogDebug("Retransmission timeout!!!")
			s.sendBuf.ResetNextSeq()
			go s.sendNextPacket()
		}
	}
}

func (s *TCPConnState) makeHeader() *TCPHeader {
	return &TCPHeader{
		SrcPort:    s.localPort,
		DstPort:    s.remotePort,
		WindowSize: s.recvBuf.Free(),

		Ack:    true,
		AckNum: uint32(s.recvStart + uint64(s.recvBuf.Len())),

		SeqNum: uint32(s.sendBuf.NextSendSeq()),
	}
}

func (s *TCPConnState) sendNextPacket() {
	s.lock.Lock()
	defer s.lock.Unlock()

	pendingPacket := false
	if s.pendingAck {
		pendingPacket = true
	} else if s.sendBuf.HasUnsentData() {
		// Data waiting to be sent.
		pendingPacket = true
	}
	if !pendingPacket {
		return
	}

	buf := make([]byte, 1024)

	hdr := s.makeHeader()
	if s.state == tcpConnStateSynRecv {
		hdr.Syn = true
		hdr.SeqNum = uint32(s.sendBuf.StartSeq() - 1)
	} else if s.state == tcpConnStateCloseWait {
		// Increment the outgoing ACK to account for the received FIN.
		hdr.AckNum++
	}
	if s.sendClosed && (s.sendBuf.Len() == 0) {
		// Sender closed and no more data to send (or ACKs to be waited on).
		hdr.Fin = true
		if s.state == tcpConnStateCloseWait {
			s.state = tcpConnStateLastAck
		}
		// TODO: Handle other states.
	}
	if s.state == tcpConnStateLastAck {
		//hdr.SeqNum++
	}
	headerSize, err := hdr.MarshalInto(buf)
	if err != nil {
		panic(err)
	}
	packetSize := headerSize
	s.pendingAck = false

	bufDataSize := len(buf) - headerSize
	if bufDataSize > 0 && s.sendBuf.HasUnsentData() {
		// We have unsent data.
		// TODO: Resend on ack timeout
		dataBuf := buf[headerSize:]
		sendOffset := int(s.sendBuf.NextSendSeq() - s.sendBuf.StartSeq())
		n := s.sendBuf.Fetch(dataBuf, sendOffset)

		packetSize += n
		s.sendBuf.SendData(n)
	}

	LogDebug("Response TCP packet: %v", hdr)

	err = s.sender.SendTCPPacket(buf[:packetSize], s.remoteAddr, s.remotePort)
	if err != nil {
		LogWarn("Error sending TCP packet: %v", err)
	}

	if s.sendBuf.HasUnsentData() {
		// More data, send another packet.
		go s.sendNextPacket()
	}
}

func isInWindow(x, windowStart, windowEnd uint32) bool {
	// Window is half-open interval: [windowStart, windowEnd)
	if windowEnd < windowStart {
		// Window wraps around.
		// [--------->end.......<start---------]
		return x >= windowStart || x < windowEnd
	}

	// Non-wrap around window
	// [.....<start------------->end.......]
	return x >= windowStart && x < windowEnd
}

func (s *TCPConnState) ConsumePacket(hdr *TCPHeader, data []byte) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	switch s.state {
	case tcpConnStateInit:
		if !hdr.Syn {
			return errors.New("Expected SYN packet")
		}

		s.recvStart = uint64(hdr.SeqNum) + 1
		s.recvBuf = ringbuffer.NewRingBuffer(make([]byte, tcpInitialWindowSize))
		s.recvWindowSize = hdr.WindowSize

		s.state = tcpConnStateSynRecv
		s.pendingAck = true
		go s.sendNextPacket()
		// No more packet processing
		return nil
	case tcpConnStateSynRecv:
		if !hdr.Ack {
			return errors.New("Expected ACK packet")
		}

		if hdr.AckNum != uint32(s.sendBuf.StartSeq()) {
			return fmt.Errorf("AckNum %d != expected %d", hdr.AckNum, s.sendBuf.StartSeq())
		}

		s.state = tcpConnStateEst
		// Continue packet processing in case this packet has data
	case tcpConnStateEst:
		if hdr.Fin {
			s.state = tcpConnStateCloseWait
			s.recvClosed = true
			s.pendingAck = true
		}

	case tcpConnStateCloseWait:

	case tcpConnStateLastAck:
		if hdr.Ack && hdr.AckNum == uint32(s.sendBuf.EndSeq()+1) {
			s.state = tcpConnStateClosed
			LogDebug("TCP CONNECTION FULLY CLOSED!!!")
			// TODO: Do something!
			return nil
		}

	case tcpConnStateClosed:
		// No nothing.
		return nil
	}

	// First, look at any ACKs.
	if hdr.Ack {
		LogDebug("BEFORE: Received ack: %d, s.sendStart: %d, sendEnd: %d",
			hdr.AckNum, s.sendBuf.StartSeq(), s.sendBuf.EndSeq())
		if s.sendBuf.Ack(hdr.AckNum) {
			// Data has been ACK'd, so inform writers since there is now
			// space in the send buffer.
			s.cond.Broadcast()

			s.lastAckTime = time.Now()
		}
		LogDebug("AFTER: Received ack: %d, s.sendStart: %d, sendEnd: %d",
			hdr.AckNum, s.sendBuf.StartSeq(), s.sendBuf.EndSeq())
	}

	// Process any incoming data.
	if len(data) > 0 {
		//LogDebug("TCP pending data: %v", data)

		recvEnd := s.recvStart + uint64(s.recvBuf.Len())
		if hdr.SeqNum != uint32(recvEnd) {
			LogDebug("Seq: %d, revcStart: %d, recvEnd: %d", hdr.SeqNum, s.recvStart, recvEnd)

			// Since the sequence number is 32-bit and wraps around, it's ambiguous
			// whether the received packet is before or after our current window. So
			// use a 1GiB threshold before and after the current receive end to
			// determine where the packet lies.
			offsetBefore := uint32(recvEnd) - hdr.SeqNum
			offsetAfter := hdr.SeqNum - uint32(recvEnd)
			LogDebug("offsetBefore: %d, offsetAfter: %d", offsetBefore, offsetAfter)

			if offsetBefore < gigabyte {
				LogInfo("Data retransmission detected. offsetBefore: %d, len(data): %d",
					offsetBefore, len(data))
				// Drop all the data
				if int(offsetBefore) < len(data) {
					data = data[int(offsetBefore):]
				} else {
					data = nil
				}
				s.pendingAck = true
			} else if offsetAfter < gigabyte {
				// TODO: Implement out-of-order packet handling.
				LogWarn("Out of order packet detected, dropping (unimplemented for now)...")
				data = nil
			} else {
				// TODO: According to RFC 9293 3.5.3 group 3, this must be responsed to
				// with an empty ACK.
				LogWarn("Data outside range, dropping...")
				data = nil
			}
		}

		if len(data) > 0 {
			// TODO: It's not clear whether a FIN packet can contain data.
			// RFC 9293 3.10.4 implies it doesn't:
			//   "Queue this until all preceding SENDs have been segmentized, then
			//   form a FIN segment and send it."
			// TODO: This doesn't work if we can only partially store a FIN packet.
			n, err := s.recvBuf.Append(data)
			if err != nil && err != ringbuffer.ErrBufferFull {
				// Don't expect any error other than ErrBufferFull
				panic(err)
			}
			if n > 0 {
				s.pendingAck = true
			}
			s.cond.Broadcast()
		}

		LogDebug("TCP read buffer length: %d, free: %d", s.recvBuf.Len(), s.recvBuf.Free())
	}

	if s.sendClosed && (s.sendBuf.Len() == 0) {
		s.pendingAck = true
	}

	if s.pendingAck {
		s.cond.Broadcast()
		go s.sendNextPacket()
	}

	return nil
}

func (s *TCPConnState) Read(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.recvBuf == nil || s.recvBuf.Len() == 0 {
		if s.recvClosed {
			// TODO: If data from the last FIN packet was only partially saved, we
			// should expect a retransmission of the remaining data and wait for it.
			return 0, io.EOF
		}
		s.cond.Wait()
	}

	readBuf := s.recvBuf.Peek(0)
	if len(readBuf) == 0 {
		panic("len(readBuf) == 0")
	}
	n := copy(b, readBuf)

	if s.recvBuf.Free() == 0 && n > 0 {
		// We're about free up some space in the receive buffer, so let the remote
		// end know.
		s.pendingAck = true
		go s.sendNextPacket()
	}
	s.recvBuf.Consume(n)
	s.recvStart += uint64(n)
	LogDebug("Read %d from TCP conn, remaining: %d", n, s.recvBuf.Len())
	return n, nil
}

func (s *TCPConnState) Write(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.sendClosed {
		return 0, io.ErrClosedPipe
	}

	written := 0
	for len(b) > 0 {
		for (s.sendBuf.Len() == s.sendBuf.Cap()) && !s.sendClosed {
			s.cond.Wait()
		}
		if s.sendClosed {
			return written, io.ErrClosedPipe
		}

		n := s.sendBuf.Append(b)
		LogDebug("Written %d to TCP conn, sendBufLen: %d", n, s.sendBuf.Len())
		written += n
		b = b[n:]
		go s.sendNextPacket()
	}

	return written, nil
}

func (s *TCPConnState) Close() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.sendClosed {
		// Already closed
		return nil
	}
	s.sendClosed = true
	s.pendingAck = true
	s.cond.Broadcast()
	go s.sendNextPacket()
	return nil
}
