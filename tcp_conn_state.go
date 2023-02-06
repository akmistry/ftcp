package dtcp

type tcpConnState int

const (
	tcpConnStateInvalid = 0
	tcpConnStateSynRecv = 1
	tcpConnStateEst     = 2

	tcpInitialWindowSize = 4096
)

type TCPConnState struct {
	state tcpConnState

	// Our sequence number, received as ack from the sender
	seqNum uint64
	// Sender's sequence number, which we send as ack.
	ackNum uint64
	// TODO: Support SACK.

	// Out window size
	windowSize int

	// Window size of the sender
	senderWindowSize int
}

func NewTCPConnState(initSeq, initAck uint32) *TCPConnState {
	s := &TCPConnState{
		seqNum:     uint64(initSeq),
		ackNum:     uint64(initAck),
		state:      tcpConnStateSynRecv,
		windowSize: tcpInitialWindowSize,
	}
	return s
}

func (s *TCPConnState) GenerateRespHeader() *TCPHeader {
	h := &TCPHeader{
		SeqNum:     uint32(s.seqNum),
		AckNum:     uint32(s.ackNum) + 1,
		Ack:        true,
		WindowSize: s.windowSize,
	}
	return h
}
