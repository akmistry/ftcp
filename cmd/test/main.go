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

	sock, err := dtcp.OpenIPSocket(ipAddr)
	if err != nil {
		panic(err)
	}
	log.Print("RAW socket opened")

	connMap := dtcp.MakeTCPConnMap()

	for {
		packet, err := sock.ReadPacket()
		if err != nil {
			log.Printf("Error reading IP packet: %v", err)
			panic(err)
		}
		if packet.Header.Protocol != unix.IPPROTO_TCP {
			// Not a TCP packet, ignore
			continue
		} else if !packet.Header.Dst.Equal(ipAddr) {
			// Dest address != listening address
			continue
		}
		tcpHeader, err := dtcp.ParseTCPHeader(packet.Payload())
		if err != nil {
			log.Printf("Error parsing TCP packet, dropping packet: %v", err)
			continue
		}
		if tcpHeader.DstPort != listenPort {
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
