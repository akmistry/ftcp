package dtcp

import (
	"errors"
	"fmt"
	"log"
	"sync"

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

	oneGig = (1 << 30)
)

type TCPConnState struct {
	localPort, remotePort uint16

	state tcpConnState

	// TODO: Support SACK.
	// Send state
	sendBuf        *ringbuffer.RingBuffer
	sendWindowSize int
	// Sequence number of first byte in the out buffer
	sendStart uint64
	// Next sequence number to be sent (not already sent and awaiting ack)
	sendSentSeq uint64

	// Receive state
	recvBuf    *ringbuffer.RingBuffer
	recvStart  uint64
	pendingAck bool

	// Window size of the sender
	senderWindowSize int

	lock sync.Mutex
	cond *sync.Cond
}

func NewTCPConnState(tcpSynHeader *TCPHeader) *TCPConnState {
	s := &TCPConnState{
		localPort:  tcpSynHeader.DstPort,
		remotePort: tcpSynHeader.SrcPort,
		sendBuf:    ringbuffer.NewRingBuffer(make([]byte, tcpInitialWindowSize)),
		// TODO: Use a SYN cookie.
		sendStart:      0,
		sendSentSeq:    0,
		sendWindowSize: tcpInitialWindowSize,
		state:          tcpConnStateInit,
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

		SeqNum: uint32(s.sendSentSeq),
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
	packetSize := hdr.MarshalSize()

	sendEnd := s.sendStart + uint64(s.sendBuf.Len())
	bufDataSize := len(buf) - packetSize
	if sendEnd > s.sendSentSeq && bufDataSize > 0 {
		// We have unsent data.
		// TODO: Resend on ack timeout
		dataBuf := buf[packetSize:]
		sendOffset := int(s.sendSentSeq - s.sendStart)
		pendingData := s.sendBuf.Peek(sendOffset)
		n := copy(dataBuf, pendingData)

		packetSize += n
		s.sendSentSeq += uint64(n)
	}

	log.Printf("Response TCP packet: %v", hdr)
	_, err := hdr.MarshalAppend(buf[:0])
	if err != nil {
		return 0, err
	}

	s.pendingAck = false

	return packetSize, nil
}

func (s *TCPConnState) PendingResponse() bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.state == tcpConnStateSynRecv {
		return true
	}
	if s.pendingAck {
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

		s.state = tcpConnStateSynRecv
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
		s.sendSentSeq++

		s.state = tcpConnStateEst
		// Continue packet processing in case this packet has data
	case tcpConnStateEst:
	}

	// First, look at any ACKs.
	trucSendStart := uint32(s.sendStart)
	if hdr.Ack && hdr.AckNum != trucSendStart {
		log.Printf("Received ack: %d, s.sendStart: %d", hdr.AckNum, s.sendStart)

		trueAckNum := uint64(hdr.AckNum) + (s.sendStart & tcpSeqHighMask)
		if hdr.AckNum < trucSendStart {
			trueAckNum += (1 << 32)
		}
		sendEnd := s.sendStart + uint64(s.sendBuf.Len())
		if trueAckNum < s.sendStart {
			log.Printf("trueAckNum %d < s.sendStart %d", trueAckNum, s.sendStart)
			return errors.New("bad ack num")
		} else if trueAckNum > sendEnd {
			log.Printf("trueAckNum %d > sendEnd %d", trueAckNum, sendEnd)
			return errors.New("bad ack num")
		}
		ackDiff := trueAckNum - s.sendStart
		if ackDiff > uint64(s.sendBuf.Len()) {
			log.Panicf("ackDiff %d > len(sendBuf) %d", ackDiff, s.sendBuf.Len())
		}

		s.sendBuf.Consume(int(ackDiff))
		s.sendStart += ackDiff
	}

	// Process any incoming data.
	if len(data) > 0 {
		log.Printf("TCP pending data: %v", data)

		recvEnd := s.recvStart + uint64(s.recvBuf.Len())
		if hdr.SeqNum != uint32(recvEnd) {
			log.Printf("Seq: %d, revcStart: %d, recvEnd: %d", hdr.SeqNum, s.recvStart, recvEnd)

			// Since the sequence number is 32-bit and wraps around, it's ambiguous
			// whether the received packet is before or after our current window. So
			// use a 1GiB threshold before and after the current receive end to
			// determine where the packet lies.
			offsetBefore := uint32(recvEnd) - hdr.SeqNum
			offsetAfter := hdr.SeqNum - uint32(recvEnd)
			log.Printf("offsetBefore: %d, offsetAfter: %d", offsetBefore, offsetAfter)

			if offsetBefore < oneGig {
				log.Printf("Data retransmission detected. offsetBefore: %d, len(data): %d",
					offsetBefore, len(data))
				// Drop all the data
				if int(offsetBefore) < len(data) {
					data = data[int(offsetBefore):]
				} else {
					data = nil
				}
				s.pendingAck = true
			} else if offsetAfter < oneGig {
				// TODO: Implement out-of-order packet handling.
				log.Printf("Data after range, dropping (for now)...")
				data = nil
			} else {
				log.Printf("Data outside range, dropping...")
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

		log.Printf("TCP read buffer length: %d", s.recvBuf.Len())
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
		log.Printf("s.recvBuf.Len(): %d", s.recvBuf.Len())
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
