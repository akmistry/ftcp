package dtcp

import (
	"net"

	"golang.org/x/sys/unix"
)

type IPSocket struct {
	conn *net.IPConn
}

func OpenIPSocket(addr net.IP) (*IPSocket, error) {
	ipAddr := &net.IPAddr{
		IP:   addr,
		Zone: "",
	}
	conn, err := net.ListenIP("ip4:tcp", ipAddr)
	if err != nil {
		return nil, err
	}

	rc, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}

	var sockOptErr error
	err = rc.Control(func(fd uintptr) {
		sockOptErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_HDRINCL, 1)
	})
	if err != nil {
		conn.Close()
		return nil, err
	} else if sockOptErr != nil {
		conn.Close()
		return nil, sockOptErr
	}

	sock := &IPSocket{
		conn: conn,
	}
	return sock, nil
}

func (s *IPSocket) Close() error {
	return s.conn.Close()
}

func (s *IPSocket) ReadPacket() (*IPPacket, error) {
	// TODO: Struct/interface for buffer reuse
	buf := make([]byte, 2048)
	n, err := s.conn.Read(buf)
	if err != nil {
		return nil, err
	}
	buf = buf[:n]
	return MakeIPPacket(buf)
}
