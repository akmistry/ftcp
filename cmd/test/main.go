package main

import (
	"errors"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/akmistry/ftcp"
)

const (
	errorRateNum   = 0
	errorRateDenom = 4
)

func doEcho(r io.Reader, w io.WriteCloser) {
	n, err := io.Copy(w, r)
	log.Printf("io.Copy n: %d, err: %v", n, err)
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

	shutdown bool
	conn     ftcp.IPConn
	lock     sync.Mutex
	cond     *sync.Cond
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

func (s *testStack) shutDown() {
	s.lock.Lock()
	s.shutdown = true
	s.cond.Signal()
	s.lock.Unlock()
}

func (s *testStack) ReadFromIP(b []byte) (int, *net.IPAddr, error) {
	s.lock.Lock()
	for s.conn == nil && !s.shutdown {
		s.cond.Wait()
	}
	s.lock.Unlock()
	if s.shutdown {
		return 0, nil, errors.New("testStack: shutdown")
	}
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
	ftcp.SetLogLevel(ftcp.LOG_INFO)

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
		go doEcho(ch, ch)
	}

}
