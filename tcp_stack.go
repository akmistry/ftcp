package ftcp

import (
	"errors"
	"io"
	"net"
)

var (
	errTcpChecksum = errors.New("tcp_stack: invalid checksum")
)

type TCPSender interface {
	SendTCPPacket(b []byte, addr *net.IPAddr, port uint16) error
}

type TCPStack struct {
	ipSender  IPSender
	localAddr *net.IPAddr
	connMap   *TCPConnMap

	connCh chan *TCPConnState
}

func NewTCPStack(ipSender IPSender, localAddr *net.IPAddr) *TCPStack {
	s := &TCPStack{
		ipSender:  ipSender,
		localAddr: localAddr,
		connMap:   MakeTCPConnMap(),
		connCh:    make(chan *TCPConnState, 64),
	}
	return s
}

func (s *TCPStack) HandleIPPacket(packet *IPPacket) error {
	// TODO: Handle multiple listeners
	listenPort := uint16(9999)

	tcpPacket := packet.Payload()
	tcpHeader, err := ParseTCPHeader(tcpPacket)
	if err != nil {
		return err
	}

	if tcpHeader.DstPort != listenPort {
		//LogDebug("Not listening to TCP dst port: %d", tcpHeader.DstPort)
		// Not the port we're listening on
		return nil
	}
	tcpData := tcpPacket[tcpHeader.DataOff:]
	LogDebug("Received TCP packet with %d bytes data: %v", len(tcpData), tcpHeader)

	// Verify TCP checksum
	calcTcpChecksum := TCPChecksum(tcpPacket, packet.Header.Src, packet.Header.Dst)
	if tcpHeader.Checksum != calcTcpChecksum {
		LogWarn("TCP checksum 0x%04x != calculated checksum 0x%04x",
			tcpHeader.Checksum, calcTcpChecksum)
		// Ignore checksum errors. There seem to be too many of them.
		//return errTcpChecksum
	}

	// TODO: Support the full 4-tuple for connection identity
	tcpConn := s.connMap.GetState(packet.Header.Src, tcpHeader.SrcPort)
	if tcpConn == nil && tcpHeader.Syn {
		tcpConn = NewTCPConnState(tcpHeader, s, &net.IPAddr{IP: packet.Header.Src})
		s.connMap.PutState(packet.Header.Src, tcpHeader.SrcPort, tcpConn)

		// TODO: This is wrong. We should only declare a new connection when we
		// receive the first ACK from the remote end, completing the 3-way
		// handshake.  For now, this is the easy way.
		s.connCh <- tcpConn
	}
	if tcpConn == nil {
		LogDebug("No TCP connection found for src %v:%d", packet.Header.Src, tcpHeader.SrcPort)
		return nil
	}

	err = tcpConn.ConsumePacket(tcpHeader, packet.Payload()[tcpHeader.DataOff:])
	if err != nil {
		LogWarn("tcpConn.ConsumePacket error: %v", err)
		return nil
	}

	return nil
}

func (s *TCPStack) SendTCPPacket(b []byte, addr *net.IPAddr, port uint16) error {
	TCPSetChecksum(b, TCPChecksum(b, addr.IP, s.localAddr.IP))

	return s.ipSender.SendIPPacket(b, addr)
}

func (s *TCPStack) Listen() (io.ReadWriteCloser, error) {
	c := <-s.connCh
	return c, nil
}
