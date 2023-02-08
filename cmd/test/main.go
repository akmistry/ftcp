package main

import (
	"io"
	"log"
	"math/rand"
	"net"

	"golang.org/x/sys/unix"

	"github.com/akmistry/ftcp"
)

const (
	maxPacketSize = 1024

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

func main() {
	ftcp.SetLogLevel(ftcp.LOG_INFO)

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
	ipStack := ftcp.NewIPStack(adapter, localAddr)
	tcpStack := ftcp.NewTCPStack(ipStack, localAddr)
	ipStack.RegisterProtocolHandler(unix.IPPROTO_TCP, tcpStack)
	go func() {
		for {
			ch, err := tcpStack.Listen()
			if err != nil {
				panic(err)
			}
			go doEcho(ch, ch)
		}
	}()

	err = ipStack.Run()
	if err != nil {
		panic(err)
	}

}
