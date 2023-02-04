package dtcp

import (
	"net"
	"sync"
)

type tcpConnKey struct {
	IP   [4]byte
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
	ip4 := ip.To4()
	if ip4 == nil {
		return tcpConnKey{}, false
	}
	key := tcpConnKey{
		Port: port,
	}
	copy(key.IP[:], ip4)
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
