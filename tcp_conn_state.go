package dtcp

type tcpConnState int

const (
	tcpConnStateInvalid = 0
	tcpConnStateSynRecv = 1
	tcpConnStateEst     = 2
)

type TCPConnState struct {
	state tcpConnState

	// Our sequence number, received as ack from the sender
	seqNum uint64
	// Sender's sequence number, which we send as ack.
	ackNum uint64
	// TODO: Support SACK.

	// Window size of the sender
	senderWindowSize int
}

func NewTCPConnState() *TCPConnState {
	s := &TCPConnState{
		state: tcpConnStateSynRecv,
	}
	return s
}
