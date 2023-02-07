package dtcp

import (
	"errors"
	"fmt"
	"log"

	"github.com/akmistry/go-util/ringbuffer"
)

type tcpConnState int

const (
	tcpConnStateInit    = 0
	tcpConnStateSynRecv = 1
	tcpConnStateEst     = 2

	tcpInitialWindowSize = 4096

	tcpSeqHighMask = 0xFFFFFFFF00000000
)

type TCPConnState struct {
	state tcpConnState

	// TODO: Support SACK.
	// Send state
	sendBuf        *ringbuffer.RingBuffer
	sendWindowSize int
	// Sequence number of first byte in the out buffer
	sendStart uint64
	// Sequence number of one past the last by in the out buffer.
	// sendEnd - sendStart = # bytes in the out buffer
	// (sendStart == sendEnd) => empty out buffer
	sendEnd uint64

	// Receive state
	recvBuf    *ringbuffer.RingBuffer
	recvStart  uint64
	recvEnd    uint64
	pendingAck bool

	// Window size of the sender
	senderWindowSize int
}

func NewTCPConnState() *TCPConnState {
	s := &TCPConnState{
		sendBuf: ringbuffer.NewRingBuffer(make([]byte, tcpInitialWindowSize)),
		// TODO: Use a SYN cookie.
		sendStart:      0,
		sendEnd:        0,
		sendWindowSize: tcpInitialWindowSize,
		state:          tcpConnStateInit,
	}
	return s
}

func (s *TCPConnState) GenerateRespHeader() *TCPHeader {
	h := &TCPHeader{
		SeqNum:     uint32(s.sendStart),
		AckNum:     uint32(s.recvEnd),
		Ack:        true,
		WindowSize: s.sendWindowSize,
	}
	s.pendingAck = false
	return h
}

func (s *TCPConnState) PendingResponse() bool {
	if s.state == tcpConnStateSynRecv {
		return true
	}
	if s.pendingAck {
		return true
	}
	return false
}

func (s *TCPConnState) ConsumePacket(hdr *TCPHeader, data []byte) error {
	switch s.state {
	case tcpConnStateInit:
		if !hdr.Syn {
			return errors.New("Expected SYN packet")
		}

		s.recvStart = uint64(hdr.SeqNum) + 1
		s.recvEnd = uint64(hdr.SeqNum) + 1
		s.recvBuf = ringbuffer.NewRingBuffer(make([]byte, hdr.WindowSize))

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
		s.sendEnd++

		s.state = tcpConnStateEst
		// Continue packet processing in case this packet has data
	case tcpConnStateEst:
	}

	// First, look at any ACKs.
	trucSendStart := uint32(s.sendStart)
	if hdr.Ack && hdr.AckNum != trucSendStart {
		log.Printf("Received ack: %d, s.sendStart: %d", hdr.AckNum, s.sendStart)

		trueAckNum := uint64(hdr.AckNum)
		if hdr.AckNum > trucSendStart {
			trueAckNum += (s.sendStart & tcpSeqHighMask)
		} else {
			trueAckNum += (s.sendEnd & tcpSeqHighMask)
		}
		if trueAckNum < s.sendStart {
			log.Printf("trueAckNum %d < s.sendStart %d", trueAckNum, s.sendStart)
			return errors.New("bad ack num")
		} else if trueAckNum > s.sendEnd {
			log.Printf("trueAckNum %d > s.sendEnd %d", trueAckNum, s.sendEnd)
			return errors.New("bad ack num")
		}
		ackDiff := trueAckNum - s.sendStart
		if ackDiff > uint64(s.sendBuf.Used()) {
			log.Panicf("ackDiff %d > len(sendBuf) %d", ackDiff, s.sendBuf.Used())
		}

		s.sendBuf.Consume(int(ackDiff))
		s.sendStart += ackDiff
	}

	// Process any incoming data.
	if len(data) > 0 {
		log.Printf("TCP pending data: %v", data)

		// TODO: Handle retransmissions.
		if hdr.SeqNum != uint32(s.recvEnd) {
			log.Printf("Retransmission detected")
		}

		n, err := s.recvBuf.Write(data)
		if err != nil && err != ringbuffer.ErrBufferFull {
			return err
		}

		s.recvEnd += uint64(n)
		s.pendingAck = true

		log.Printf("TCP read buffer length: %d", s.recvBuf.Len())
	}

	return nil
}
