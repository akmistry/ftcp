package main

import (
	"log"
	"net"

	"github.com/akmistry/dtcp"
)

func main() {
	ipAddr := net.IPv4(192, 168, 1, 134)

	sock, err := dtcp.OpenIPSocket(ipAddr)
	if err != nil {
		panic(err)
	}
	log.Print("RAW socket opened")

	for i := 0; i < 10; i++ {
		packet, err := sock.ReadPacket()
		if err != nil {
			panic(err)
		}
		tcpHeader, err := dtcp.ParseTCPHeader(packet.Payload())
		if err != nil {
			panic(err)
		}
		log.Printf("Read packet: %v", packet)
		log.Printf("Payload: %v", packet.Payload())
		log.Printf("TCP header: %v", tcpHeader)
	}

}
