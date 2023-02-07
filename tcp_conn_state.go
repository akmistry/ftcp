package dtcp

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/akmistry/go-util/ringbuffer"
)

type tcpConnState int

const (
	tcpConnStateInit    = 0
	tcpConnStateSynRecv = 1
	tcpConnStateEst     = 2

	tcpInitialWindowSize = 4096

	tcpSeqHighMask = 0xFFFFFFFF00000000

	tcpMinPacketSize = 20

	// Use a static 1 second retransmission timeout
	tcpRto = time.Second

	gigabyte = (1 << 30)
)

type TCPConnState struct {
	localPort, remotePort uint16

	state tcpConnState

	// TODO: Support SACK.
	// Send state
	sendBuf *ringbuffer.RingBuffer
	// Sequence number of first byte in the out buffer
	sendStart uint64
	// Next sequence number to be sent (not already sent and awaiting ack)
	sendNextSeq uint64

	// Receive state
	recvBuf        *ringbuffer.RingBuffer
	recvWindowSize int
	recvStart      uint64
	pendingAck     bool

	lock sync.Mutex
	cond *sync.Cond
}

func NewTCPConnState(tcpSynHeader *TCPHeader) *TCPConnState {
	s := &TCPConnState{
		localPort:  tcpSynHeader.DstPort,
		remotePort: tcpSynHeader.SrcPort,
		sendBuf:    ringbuffer.NewRingBuffer(make([]byte, tcpInitialWindowSize)),
		// TODO: Use a SYN cookie.
		sendStart:   0,
		sendNextSeq: 0,
		state:       tcpConnStateInit,
	}
	s.cond = sync.NewCond(&s.lock)
	return s
}

func (s *TCPConnState) makeReponseHeader() *TCPHeader {
	return &TCPHeader{
		SrcPort:    s.localPort,
		DstPort:    s.remotePort,
		WindowSize: s.recvBuf.Free(),

		Ack:    true,
		AckNum: uint32(s.recvStart + uint64(s.recvBuf.Len())),

		SeqNum: uint32(s.sendNextSeq),
	}
}

func (s *TCPConnState) MakePacket(buf []byte) (int, error) {
	if len(buf) < tcpMinPacketSize {
		panic("len(buf) < tcpMinPacketSize (20)")
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	hdr := s.makeReponseHeader()
	if s.state == tcpConnStateSynRecv {
		hdr.Syn = true
	}
	headerSize, err := hdr.MarshalInto(buf)
	if err != nil {
		return 0, err
	}
	packetSize := headerSize
	s.pendingAck = false

	sendEnd := s.sendStart + uint64(s.sendBuf.Len())
	bufDataSize := len(buf) - headerSize
	if sendEnd > s.sendNextSeq && bufDataSize > 0 {
		// We have unsent data.
		// TODO: Resend on ack timeout
		dataBuf := buf[headerSize:]
		sendOffset := int(s.sendNextSeq - s.sendStart)
		pendingData := s.sendBuf.Peek(sendOffset)
		n := copy(dataBuf, pendingData)

		packetSize += n
		s.sendNextSeq += uint64(n)
	}

	LogDebug("Response TCP packet: %v", hdr)

	return packetSize, nil
}

func (s *TCPConnState) PendingResponse() bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.pendingAck {
		return true
	} else if s.sendNextSeq < (s.sendStart + uint64(s.sendBuf.Len())) {
		// Data waiting to be sent.
		return true
	}
	return false
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
		// No more packet processing
		return nil
	case tcpConnStateSynRecv:
		if !hdr.Ack {
			return errors.New("Expected ACK packet")
		}

		expectedAck := uint32(s.sendStart + 1)
		if hdr.AckNum != expectedAck {
			return fmt.Errorf("AckNum %d != expected %d", hdr.AckNum, expectedAck)
		}
		s.sendStart++
		s.sendNextSeq++

		s.state = tcpConnStateEst
		// Continue packet processing in case this packet has data
	case tcpConnStateEst:
	}

	// First, look at any ACKs.
	trucSendStart := uint32(s.sendStart)
	if hdr.Ack && hdr.AckNum != trucSendStart {
		LogDebug("Received ack: %d, s.sendStart: %d", hdr.AckNum, s.sendStart)

		trueAckNum := uint64(hdr.AckNum) + (s.sendStart & tcpSeqHighMask)
		if hdr.AckNum < trucSendStart {
			trueAckNum += (1 << 32)
		}
		sendEnd := s.sendStart + uint64(s.sendBuf.Len())
		if trueAckNum < s.sendStart {
			LogWarn("trueAckNum %d < s.sendStart %d", trueAckNum, s.sendStart)
			return errors.New("bad ack num")
		} else if trueAckNum > sendEnd {
			LogWarn("trueAckNum %d > sendEnd %d", trueAckNum, sendEnd)
			return errors.New("bad ack num")
		}
		ackDiff := trueAckNum - s.sendStart
		if ackDiff > uint64(s.sendBuf.Len()) {
			LogFatal("ackDiff %d > len(sendBuf) %d", ackDiff, s.sendBuf.Len())
		}

		s.sendBuf.Consume(int(ackDiff))
		s.sendStart += ackDiff
	}

	// Process any incoming data.
	if len(data) > 0 {
		LogDebug("TCP pending data: %v", data)

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
				LogWarn("Data outside range, dropping...")
				data = nil
			}
		}

		if len(data) > 0 {
			n, err := s.recvBuf.Write(data)
			if err != nil && err != ringbuffer.ErrBufferFull {
				// Don't expect any error other than ErrBufferFull
				panic(err)
			}
			if n > 0 {
				s.pendingAck = true
			}
		}

		LogDebug("TCP read buffer length: %d", s.recvBuf.Len())
	}

	if s.pendingAck {
		s.cond.Signal()
	}

	return nil
}

func (s *TCPConnState) Read(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.recvBuf == nil || s.recvBuf.Len() == 0 {
		s.cond.Wait()
	}

	readBuf := s.recvBuf.Peek(0)
	if len(readBuf) == 0 {
		panic("len(readBuf) == 0")
	}
	n := copy(b, readBuf)
	s.recvBuf.Consume(n)
	s.recvStart += uint64(n)
	return n, nil
}

func (s *TCPConnState) Write(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	written := 0
	for len(b) > 0 {
		for s.sendBuf.Free() == 0 {
			// TODO: Check for connection close.
			s.cond.Wait()
		}

		n, err := s.sendBuf.Write(b)
		written += n
		b = b[n:]
		if err != nil && err != ringbuffer.ErrBufferFull {
			return written, err
		}
	}

	return written, nil
}
