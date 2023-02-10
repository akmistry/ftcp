package ftcp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/akmistry/ftcp/pb"
)

type tcpConnState = pb.TcpConnState

const (
	// TCP connection state machine (RFC 9293 3.3.2 (mostly))
	tcpConnStateInit     = pb.TcpConnState_INIT
	tcpConnStateSynRecv  = pb.TcpConnState_SYN_RECV
	tcpConnStateEst      = pb.TcpConnState_EST
	tcpConnStateFinWait1 = pb.TcpConnState_FIN_WAIT_1
	tcpConnStateFinWait2 = pb.TcpConnState_FIN_WAIT_2
	// Received FIN from remote end, waiting for close from the user.
	tcpConnStateCloseWait = pb.TcpConnState_CLOSE_WAIT
	tcpConnStateClosing   = pb.TcpConnState_CLOSING
	// Received and sent FIN and waiting on FIN-ACK from remote.
	tcpConnStateLastAck  = pb.TcpConnState_LAST_ACK
	tcpConnStateTimeWait = pb.TcpConnState_TIME_WAIT
	// Any connection in this state should be removed.
	tcpConnStateClosed = pb.TcpConnState_CLOSED
)

const (
	// Initial window size
	// TODO: Increase this
	tcpInitialWindowSize = 4096

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
	recvBuf    *SyncedBuffer
	recvClosed bool

	// Last ACK receive time, for the retransmission timeout
	lastAckTime time.Time

	// There is a pending outgoing packet
	pendingSend     bool
	pendingSendCond *sync.Cond

	pendingSync bool

	// Last synced sequence numbers of send and recv buffers
	sendSyncSeq uint64
	recvSyncSeq uint64

	lock sync.Mutex
	// TODO: Seperate condvars for send and receive buffers
	cond *sync.Cond
}

func NewTCPConnState(localPort, remotePort uint16, sender TCPSender, remoteAddr *net.IPAddr) *TCPConnState {
	s := &TCPConnState{
		sender:     sender,
		localPort:  localPort,
		remotePort: remotePort,
		remoteAddr: remoteAddr,
		sendBuf:    NewSyncedBuffer(1, tcpInitialWindowSize),
		// TODO: Use a SYN cookie.
		state: tcpConnStateInit,
	}
	s.cond = sync.NewCond(&s.lock)
	s.pendingSendCond = sync.NewCond(&s.lock)
	go s.retransmitTimeout()
	go s.packetSender()
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

		if now.Sub(s.lastAckTime) > tcpRto && s.sendBuf.Len() > 0 {
			LogDebug("Retransmission timeout!!!")
			s.sendBuf.ResetNextSeq()
			s.triggerSendPacket()
		}
	}
}

func (s *TCPConnState) makeHeader() *TCPHeader {
	return &TCPHeader{
		SrcPort:    s.localPort,
		DstPort:    s.remotePort,
		WindowSize: s.recvBuf.Free(),

		Ack:    true,
		AckNum: uint32(s.recvBuf.EndSeq()),

		SeqNum: uint32(s.sendBuf.NextSendSeq()),
	}
}

func (s *TCPConnState) packetSender() {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.state != tcpConnStateClosed {
		s.sendSyncRequest()

		s.trySendNextPacket()

		for s.state != tcpConnStateClosed && !s.pendingSend {
			s.pendingSendCond.Wait()
		}
	}

}

func (s *TCPConnState) triggerSendPacket() {
	if !s.pendingSend {
		s.pendingSend = true
		s.pendingSendCond.Signal()
	}
}

func (s *TCPConnState) trySendNextPacket() {
	if !s.pendingSend {
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
	s.pendingSend = false

	bufDataSize := len(buf) - headerSize
	if bufDataSize > 0 && s.sendBuf.HasUnsentData() {
		// We have unsent data.
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
		s.pendingSend = true
	}
}

func (s *TCPConnState) ConsumePacket(hdr *TCPHeader, data []byte) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	switch s.state {
	case tcpConnStateInit:
		if !hdr.Syn {
			return errors.New("Expected SYN packet")
		}

		s.recvBuf = NewSyncedBuffer(hdr.SeqNum+1, tcpInitialWindowSize)

		s.state = tcpConnStateSynRecv
		s.triggerSendPacket()
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
		s.pendingSync = true
		// Continue packet processing in case this packet has data
	case tcpConnStateEst:
		// Ignore FIN if the packet contains data
		if hdr.Fin && len(data) == 0 {
			// Check the sequence number to make sure we've received all the data
			// expected. If not, ignore the FIN until we have all the data expected.
			recvEnd := uint32(s.recvBuf.EndSeq())
			if hdr.SeqNum == recvEnd {
				s.state = tcpConnStateCloseWait
				s.recvClosed = true
				s.pendingSync = true
				s.triggerSendPacket()
			}
		}

	case tcpConnStateCloseWait:

	case tcpConnStateLastAck:
		if hdr.Ack && hdr.AckNum == uint32(s.sendBuf.EndSeq()+1) {
			s.state = tcpConnStateClosed
			s.pendingSync = true
			s.triggerSendPacket()
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
			// Data has been ACK'd, so inform writers there is now space in the send
			// buffer.
			s.cond.Broadcast()

			s.lastAckTime = time.Now()
			s.pendingSync = true
		}
		LogDebug("AFTER: Received ack: %d, s.sendStart: %d, sendEnd: %d",
			hdr.AckNum, s.sendBuf.StartSeq(), s.sendBuf.EndSeq())
	}

	// Process any incoming data.
	if len(data) > 0 {
		//LogDebug("TCP pending data: %v", data)

		recvEnd := s.recvBuf.EndSeq()
		if hdr.SeqNum != uint32(recvEnd) {
			LogDebug("Seq: %d, revcStart: %d, recvEnd: %d",
				hdr.SeqNum, s.recvBuf.StartSeq(), recvEnd)

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
				s.triggerSendPacket()
			} else if offsetAfter < gigabyte {
				// TODO: Implement out-of-order packet handling.
				LogWarn("Out of order packet detected, dropping (unimplemented for now)...")
				data = nil
			} else {
				LogWarn("Data outside range, dropping...")
				data = nil
				// TODO: According to RFC 9293 3.5.2 group 3, this must be responded to
				// with an __empty__ ACK.
				s.triggerSendPacket()
			}
		}

		if len(data) > 0 {
			n := s.recvBuf.Append(data)
			if n > 0 {
				// New data in the receive buffer, signal any waiting readers.
				s.cond.Broadcast()
				// Send an ACK.
				s.pendingSync = true
				s.triggerSendPacket()
			}
		}

		LogDebug("TCP read buffer length: %d, free: %d", s.recvBuf.Len(), s.recvBuf.Free())
	}

	if s.sendClosed && (s.sendBuf.Len() == 0) {
		s.triggerSendPacket()
	}

	return nil
}

func (s *TCPConnState) Read(b []byte) (int, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	for s.recvBuf == nil || s.recvBuf.Len() == 0 {
		if s.recvClosed {
			return 0, io.EOF
		}
		s.cond.Wait()
	}

	n := s.recvBuf.Fetch(b, 0)

	if s.recvBuf.Free() == 0 && n > 0 {
		// We're about free up some space in the receive buffer, so let the remote
		// end know.
		s.triggerSendPacket()
	}
	s.recvBuf.Consume(n)
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
		s.pendingSync = true
		s.triggerSendPacket()
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
	s.cond.Broadcast()
	// Potentially send FIN if there is no data in the send buffer.
	s.triggerSendPacket()
	return nil
}

func (s *TCPConnState) sendSyncRequest() {
	if !s.pendingSync {
		return
	}

	req := pb.SyncRequest{
		Key: &pb.StreamKey{
			RemoteAddr: s.remoteAddr.IP,
			RemotePort: uint32(s.remotePort),
			LocalPort:  uint32(s.localPort),
		},
		State:         s.state,
		SendBufUpdate: &pb.BufferStateUpdate{},
		RecvBufUpdate: &pb.BufferStateUpdate{},
	}
	s.sendBuf.FillStateUpdate(req.SendBufUpdate)
	s.recvBuf.FillStateUpdate(req.RecvBufUpdate)

	if s.sendBuf.EndSeq() > s.sendSyncSeq {
		if s.sendSyncSeq < s.sendBuf.StartSeq() {
			s.sendSyncSeq = s.sendBuf.StartSeq()
		}
		appendLen := int(s.sendBuf.EndSeq() - s.sendSyncSeq)
		if appendLen > 0 {
			req.SendBufUpdate.AppendSeq = s.sendSyncSeq
			req.SendBufUpdate.AppendData = make([]byte, appendLen)
			s.sendBuf.Fetch(req.SendBufUpdate.AppendData, int(s.sendSyncSeq-s.sendBuf.StartSeq()))
		}
		s.sendSyncSeq = s.sendBuf.EndSeq()
	}
	if s.recvBuf.EndSeq() > s.recvSyncSeq {
		if s.recvSyncSeq < s.recvBuf.StartSeq() {
			s.recvSyncSeq = s.recvBuf.StartSeq()
		}
		appendLen := int(s.recvBuf.EndSeq() - s.recvSyncSeq)
		if appendLen > 0 {
			req.RecvBufUpdate.AppendSeq = s.recvSyncSeq
			req.RecvBufUpdate.AppendData = make([]byte, appendLen)
			s.recvBuf.Fetch(req.RecvBufUpdate.AppendData, int(s.recvSyncSeq-s.recvBuf.StartSeq()))
		}
		s.recvSyncSeq = s.recvBuf.EndSeq()
	}
	s.pendingSync = false
	LogInfo("TCPConnState: Update request: %+v", req)

	reply := pb.SyncReply{}
	s.lock.Unlock()
	err := s.sender.SendSyncRequest(&req, &reply)
	if err != nil {
		LogWarn("TCPConnState: error doing Sync: %v", err)
	} else {
		LogInfo("TCPConnState: Update reply: %+v", reply)
	}
	s.lock.Lock()

	// TODO: When the reply is received, it will tell us if the replica is behind
	// and needs us to send it more data. See the last comment in
	// SyncedBuffer.UpdateState() for details.
}

func (s *TCPConnState) syncState(reqState tcpConnState) {
	// TODO: Handle more subtle state transitions
	if reqState < s.state {
		return
	}

	if reqState > tcpConnStateCloseWait {
		s.recvClosed = true
	}
	s.state = reqState
}

func (s *TCPConnState) Sync(req *pb.SyncRequest, reply *pb.SyncReply) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	LogInfo("TCPConnState: received sync request: %+v", *req)

	if req.SendBufUpdate != nil {
		reply.SendBufUpdate = &pb.BufferStateUpdate{}
		s.sendBuf.Sync(req.SendBufUpdate, reply.SendBufUpdate)
	}

	if s.state == tcpConnStateInit && req.State > tcpConnStateInit {
		// The sequence number will be fast-forwarded on the recv buffer update
		// below.
		s.recvBuf = NewSyncedBuffer(0, tcpInitialWindowSize)
		s.state = tcpConnStateSynRecv
	}
	if req.RecvBufUpdate != nil {
		reply.RecvBufUpdate = &pb.BufferStateUpdate{}
		s.recvBuf.Sync(req.RecvBufUpdate, reply.RecvBufUpdate)
	}

	if req.State != pb.TcpConnState_INIT {
		s.syncState(req.State)
	}
	reply.State = s.state

	return nil
}
