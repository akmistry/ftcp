package dtcp

import (
	"math/rand"
	"net"
	"sync"

	"golang.org/x/net/ipv4"
)

const (
	ipStackMaxPacketSize = 65536
)

type IPConn interface {
	ReadFromIP(b []byte) (int, *net.IPAddr, error)
	WriteToIP(b []byte, addr *net.IPAddr) (int, error)
}

type IPSender interface {
	SendIPPacket(b []byte, addr *net.IPAddr) error
}

type IPProtcolHandler interface {
	HandleIPPacket(packet *IPPacket) error
}

type IPStack struct {
	conn       IPConn
	listenAddr *net.IPAddr

	protoHandlers map[int]IPProtcolHandler
	lock          sync.Mutex
}

func NewIPStack(conn IPConn, listenAddr *net.IPAddr) *IPStack {
	s := &IPStack{
		conn:          conn,
		listenAddr:    listenAddr,
		protoHandlers: make(map[int]IPProtcolHandler),
	}
	return s
}

func (s *IPStack) RegisterProtocolHandler(proto int, handler IPProtcolHandler) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.protoHandlers[proto] != nil {
		panic("Protocol handler already registered")
	}
	s.protoHandlers[proto] = handler
}

func (s *IPStack) Run() error {
	packetBuf := make([]byte, ipStackMaxPacketSize)

	for {
		n, _, err := s.conn.ReadFromIP(packetBuf)
		if err != nil {
			LogError("Error reading IP packet: %v", err)
			return err
		}
		readPacket := packetBuf[:n]
		ipPacket, err := MakeIPPacket(readPacket)
		if err != nil {
			LogWarn("Error parsing IP packet: %v", err)
			continue
		}
		if !ipPacket.Header.Dst.Equal(s.listenAddr.IP) {
			// Not listening on this address. Drop packet.
			continue
		}

		// Verify IP checksum
		calcIpChecksum := IPChecksum(readPacket[:ipPacket.Header.Len])
		if uint16(ipPacket.Header.Checksum) != calcIpChecksum {
			LogWarn("IP checksum 0x%04x != calculated checksum 0x%04x",
				ipPacket.Header.Checksum, calcIpChecksum)
			continue
		}

		s.lock.Lock()
		handler := s.protoHandlers[ipPacket.Header.Protocol]
		s.lock.Unlock()

		if handler == nil {
			LogDebug("No handler registered for protocol %d", ipPacket.Header.Protocol)
			continue
		}
		err = handler.HandleIPPacket(ipPacket)
		if err != nil {
			LogWarn("Error handling IP packet: %v", err)
		}
	}
}

func (s *IPStack) SendIPPacket(b []byte, addr *net.IPAddr) error {
	respIpHeader := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TotalLen: ipv4.HeaderLen + len(b),
		FragOff:  0,
		TTL:      32,
		Protocol: 6,
		ID:       rand.Int(),
		Src:      s.listenAddr.IP,
		Dst:      addr.IP,
	}
	packetBuf, err := respIpHeader.Marshal()
	if err != nil {
		panic(err)
	}
	IPSetChecksum(packetBuf, IPChecksum(packetBuf))
	packetBuf = append(packetBuf, b...)

	_, err = s.conn.WriteToIP(packetBuf, addr)
	return err
}
