package dtcp

import (
	"io"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func OpenIPSocket(addr net.IP) (*net.IPConn, error) {
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
	return conn, nil
}

func htons(v uint16) uint16 {
	return ((v & 0xFF) << 8) | ((v & 0xFF00) >> 8)
}

func OpenRawSocket() (io.ReadWriteCloser, error) {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM, int(htons(unix.ETH_P_IP)))
	if err != nil {
		return nil, err
	}

	/*
		ifs, err := net.Interfaces()
		if err != nil {
			return nil, err
		}
		for _, ifi := range ifs {
			log.Printf("Interface: %v", ifi)
		}
	*/
	ifc, err := net.InterfaceByName("eno1")
	if err != nil {
		LogError("Interface error: %v", err)
		return nil, err
	}
	sa := &unix.SockaddrLinklayer{
		Protocol: htons(unix.ETH_P_IP),
		Ifindex:  ifc.Index,
	}
	err = unix.Bind(fd, sa)
	if err != nil {
		LogError("Bind error: %v", err)
		return nil, err
	}

	return os.NewFile(uintptr(fd), "<raw socket>"), nil
}
