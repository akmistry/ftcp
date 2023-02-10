package main

import (
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/akmistry/ftcp"
)

const (
	errorRateNum   = 0
	errorRateDenom = 4
)

type StatelessConn interface {
	ConsumeThenRead(b []byte, offset uint64) (int, error)
	AppendAt(b []byte, offset uint64) (int, error)
	Close() error
}

func doEcho(r StatelessConn, w StatelessConn, readBytes *int) {
	buf := make([]byte, 4096)
	offset := uint64(0)

	for {
		read, err := r.ConsumeThenRead(buf, offset)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}
		*readBytes += read

		// Randomly fail after the read to simulate an application failure
		if rand.Intn(4) < 1 {
			log.Print("AFTER READ FAILURE SIMULATED")
			continue
		}

		written, err := w.AppendAt(buf[:read], offset)
		if err != nil {
			panic(err)
		}
		if read != written {
			panic("read != written")
		}

		// Randonly don't persist "state" to simulate an application failure
		if rand.Intn(4) < 1 {
			log.Print("STATE PERSISTENCE FAILURE SIMULATED")
			continue
		}
		offset += uint64(written)
	}
	w.Close()
}

func dropPacket() bool {
	n := rand.Intn(errorRateDenom)
	return n < errorRateNum
}

type socketAdapter struct {
	rawSock    io.ReadWriteCloser
	ipSendSock *net.IPConn
}

func (a *socketAdapter) ReadFromIP(b []byte) (int, *net.IPAddr, error) {
	for {
		n, err := a.rawSock.Read(b)
		if dropPacket() {
			continue
		}
		return n, nil, err
	}
}

func (a *socketAdapter) WriteToIP(b []byte, addr *net.IPAddr) (int, error) {
	if dropPacket() {
		return len(b), nil
	}
	return a.ipSendSock.WriteToIP(b, addr)
}

type testStack struct {
	ipStack  *ftcp.IPStack
	tcpStack *ftcp.TCPStack

	syncServ   *ftcp.SyncServer
	syncClient *ftcp.SyncClient

	conn ftcp.IPConn
	lock sync.Mutex
	cond *sync.Cond
}

func newTestStack(lAddr *net.IPAddr) *testStack {
	s := &testStack{}
	s.ipStack = ftcp.NewIPStack(s, lAddr)
	s.tcpStack = ftcp.NewTCPStack(s.ipStack, lAddr)
	s.ipStack.RegisterProtocolHandler(unix.IPPROTO_TCP, s.tcpStack)
	s.cond = sync.NewCond(&s.lock)
	return s
}

func (s *testStack) setConn(conn ftcp.IPConn) {
	s.lock.Lock()
	s.conn = conn
	s.cond.Signal()
	s.lock.Unlock()
}

func (s *testStack) setupSync(listenAddr, remoteAddr string) {
	l, err := net.Listen("tcp", listenAddr)
	if err != nil {
		panic(err)
	}
	s.syncServ = ftcp.NewSyncServer(l, s.tcpStack)

	s.syncClient = ftcp.NewSyncClient(remoteAddr)
	s.tcpStack.SetSyncClient(s.syncClient)
}

func (s *testStack) run() {
	err := s.ipStack.Run()
	if err != nil {
		panic(err)
	}
}

func (s *testStack) ReadFromIP(b []byte) (int, *net.IPAddr, error) {
	s.lock.Lock()
	for s.conn == nil {
		s.cond.Wait()
	}
	s.lock.Unlock()
	return s.conn.ReadFromIP(b)
}

func (s *testStack) WriteToIP(b []byte, addr *net.IPAddr) (int, error) {
	s.lock.Lock()
	conn := s.conn
	s.lock.Unlock()
	if conn == nil {
		// Lie!!!
		return len(b), nil
	}
	return conn.WriteToIP(b, addr)
}

func main() {
	ftcp.SetLogLevel(ftcp.LOG_DEBUG)

	go func() {
		log.Println("http.ListenAndServe: ", http.ListenAndServe("localhost:6060", nil))
	}()

	ipAddr := net.IPv4(192, 168, 1, 134)

	//listenIpAddr := net.IPv4(0, 0, 0, 0)
	//listenIpAddr := ipAddr
	//sock, err := ftcp.OpenIPSocket(listenIpAddr)
	sock, err := ftcp.OpenRawSocket()
	if err != nil {
		panic(err)
	}
	log.Print("RAW socket opened")

	sendSock, err := ftcp.OpenIPSocket(net.IPv4(0, 0, 0, 0))
	if err != nil {
		panic(err)
	}

	adapter := &socketAdapter{
		rawSock:    sock,
		ipSendSock: sendSock,
	}
	localAddr := &net.IPAddr{
		IP: ipAddr,
	}

	ts1 := newTestStack(localAddr)
	ts1.setupSync("127.0.0.1:9991", "127.0.0.1:9992")
	go ts1.run()

	ts2 := newTestStack(localAddr)
	ts2.setupSync("127.0.0.1:9992", "127.0.0.1:9991")
	go ts2.run()

	ts1.setConn(adapter)
	for {
		ch, err := ts1.tcpStack.Listen()
		if err != nil {
			panic(err)
		}
		readBytes := 0
		go doEcho(ch, ch, &readBytes)
		go func() {
			for {
				time.Sleep(10 * time.Millisecond)
				if readBytes > 30000 {
					break
				}
			}
			log.Print("SWITCHING CONNECTIONS")
			ts1.setConn(nil)
			ts2.setConn(adapter)
		}()
	}

}
