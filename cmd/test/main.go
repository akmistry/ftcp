package main

import (
	"log"
	"net"

	"golang.org/x/net/ipv4"
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

	sendSock, err := dtcp.OpenIPSocket(net.IPv4(0, 0, 0, 0))
	if err != nil {
		panic(err)
	}

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
		calcTcpChecksum := dtcp.TCPChecksum(packet.Payload(), packet.Header.Src, packet.Header.Dst)
		if tcpHeader.Checksum != calcTcpChecksum {
			log.Printf("TCP checksum 0x%04x != calculated checksum 0x%04x",
				tcpHeader.Checksum, calcTcpChecksum)
			continue
		}

		log.Printf("Read packet: %v", packet)
		log.Printf("Payload: %v", packet.Payload())
		log.Printf("TCP header: %v", tcpHeader)

		var tcpConn *dtcp.TCPConnState
		if tcpHeader.Syn {
			log.Print("New TCP connection")
			// SYN packet, so this is a new connection.
			tcpConn = dtcp.NewTCPConnState(0, tcpHeader.SeqNum)
			connMap.PutState(packet.Header.Src, tcpHeader.SrcPort, tcpConn)
		} else {
			log.Print("Existing TCP connection")
			tcpConn = connMap.GetState(packet.Header.Src, tcpHeader.SrcPort)
		}
		if tcpConn == nil {
			// No active connection found, drop packet.
			continue
		}

		resp := tcpConn.GenerateRespHeader()
		resp.SrcPort = tcpHeader.DstPort
		resp.DstPort = tcpHeader.SrcPort
		resp.Syn = tcpHeader.Syn

		respIp := &ipv4.Header{
			Version:  ipv4.Version,
			Len:      ipv4.HeaderLen,
			TotalLen: 20 + 20,
			FragOff:  0,
			TTL:      32,
			Protocol: 6,
			Src:      ipAddr,
			Dst:      packet.Header.Src.To4(),
		}
		respIpHeader, err := respIp.Marshal()
		if err != nil {
			panic(err)
		}
		dtcp.IPSetChecksum(respIpHeader, dtcp.IPChecksum(respIpHeader))
		respPacket, err := resp.MarshalAppend(respIpHeader)
		if err != nil {
			panic(err)
		}
		tcpRespPacket := respPacket[len(respIpHeader):]
		dtcp.TCPSetChecksum(tcpRespPacket, dtcp.TCPChecksum(tcpRespPacket, packet.Header.Dst, packet.Header.Src))

		log.Printf("Sending response to: %v", packet.Header.Src)
		_, err = sendSock.WriteToIP(respPacket, &net.IPAddr{IP: packet.Header.Src})
		log.Printf("TCP response error: %v", err)
	}

}
