package main

import (
	"log"
	"net"

	"golang.org/x/sys/unix"

	"github.com/akmistry/dtcp"
)

func main() {
	ipAddr := net.IPv4(192, 168, 1, 134)
	listenPort := uint16(9999)

	//listenIpAddr := net.IPv4(0, 0, 0, 0)
	//listenIpAddr := ipAddr
	//sock, err := dtcp.OpenIPSocket(listenIpAddr)
	sock, err := dtcp.OpenRawSocket()
	if err != nil {
		panic(err)
	}
	log.Print("RAW socket opened")

	connMap := dtcp.MakeTCPConnMap()

	buf := make([]byte, 65536)
	for {
		n, err := sock.Read(buf)
		if err != nil {
			log.Printf("Error reading IP packet: %v", err)
			panic(err)
		}
		packet, err := dtcp.MakeIPPacket(buf[:n])
		if err != nil {
			log.Printf("Error parsing IP packet: %v", err)
			continue
		}
		if packet.Header.Protocol != unix.IPPROTO_TCP {
			//log.Printf("IP protocl: %d", packet.Header.Protocol)
			// Not a TCP packet, ignore
			continue
		} else if !packet.Header.Dst.Equal(ipAddr) {
			//log.Printf("IP dst: %v", packet.Header.Dst)
			// Dest address != listening address
			continue
		}
		tcpHeader, err := dtcp.ParseTCPHeader(packet.Payload())
		if err != nil {
			log.Printf("Error parsing TCP packet, dropping packet: %v", err)
			continue
		}
		if tcpHeader.DstPort != listenPort {
			//log.Printf("TCP dst port: %d", tcpHeader.DstPort)
			// Not the port we're listening on
			continue
		}

		log.Printf("Read packet: %v", packet)
		log.Printf("Payload: %v", packet.Payload())
		log.Printf("TCP header: %v", tcpHeader)

		var tcpConn *dtcp.TCPConnState
		if tcpHeader.Syn {
			// SYN packet, so this is a new connection.
			tcpConn = dtcp.NewTCPConnState()
			connMap.PutState(packet.Header.Src, tcpHeader.SrcPort, tcpConn)
		} else {
			tcpConn = connMap.GetState(packet.Header.Src, tcpHeader.SrcPort)
		}
		if tcpConn == nil {
			// No active connection found, drop packet.
			continue
		}
	}

}
