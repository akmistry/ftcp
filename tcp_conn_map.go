package ftcp

import (
	"net"
	"net/netip"
	"sync"
)

type tcpConnKey struct {
	Addr netip.Addr
	Port uint16
}

type TCPConnMap struct {
	conns map[tcpConnKey]*TCPConnState
	lock  sync.Mutex
}

func MakeTCPConnMap() *TCPConnMap {
	return &TCPConnMap{
		conns: make(map[tcpConnKey]*TCPConnState),
	}
}

func makeTcpConnKey(ip net.IP, port uint16) (tcpConnKey, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return tcpConnKey{}, false
	}
	key := tcpConnKey{
		Addr: addr,
		Port: port,
	}
	return key, true
}

func (m *TCPConnMap) GetState(ip net.IP, port uint16) *TCPConnState {
	key, ok := makeTcpConnKey(ip, port)
	if !ok {
		return nil
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	return m.conns[key]
}

func (m *TCPConnMap) PutState(ip net.IP, port uint16, state *TCPConnState) {
	key, ok := makeTcpConnKey(ip, port)
	if !ok {
		return
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	m.conns[key] = state
}
