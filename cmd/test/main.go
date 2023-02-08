package main

import (
	"io"
	"log"
	"math/rand"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"

	"github.com/akmistry/dtcp"
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

func main() {
	dtcp.SetLogLevel(dtcp.LOG_DEBUG)

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

		if dropPacket() {
			log.Print("In packet dropped")
			continue
		}

		var tcpConn *dtcp.TCPConnState
		if tcpHeader.Syn {
			log.Print("New TCP connection")
			// SYN packet, so this is a new connection.
			tcpConn = dtcp.NewTCPConnState(tcpHeader)
			connMap.PutState(packet.Header.Src, tcpHeader.SrcPort, tcpConn)

			go doEcho(tcpConn, tcpConn)
		} else {
			log.Print("Existing TCP connection")
			tcpConn = connMap.GetState(packet.Header.Src, tcpHeader.SrcPort)
		}
		if tcpConn == nil {
			// No active connection found, drop packet.
			continue
		}

		err = tcpConn.ConsumePacket(tcpHeader, packet.Payload()[tcpHeader.DataOff:])
		if err != nil {
			log.Printf("tcpConn.ConsumePacket error: %v", err)
			continue
		}

		for tcpConn.PendingResponse() {
			respPacket := make([]byte, maxPacketSize)
			// Reserve 20 bytes for the IP header
			// TODO: Support IP options
			respTcpPacket := respPacket[20:]
			n, err := tcpConn.MakePacket(respTcpPacket)
			if err != nil {
				panic(err)
			}
			respPacket = respPacket[:n+20]
			respTcpPacket = respTcpPacket[:n]
			log.Printf("Response packet length: %d", len(respPacket))

			respIp := &ipv4.Header{
				Version:  ipv4.Version,
				Len:      ipv4.HeaderLen,
				TotalLen: len(respPacket),
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
			copy(respPacket, respIpHeader)

			dtcp.TCPSetChecksum(respTcpPacket, dtcp.TCPChecksum(respTcpPacket, packet.Header.Dst, packet.Header.Src))

			if dropPacket() {
				log.Print("Out packet dropped")
				continue
			}

			log.Printf("Sending response to: %v", packet.Header.Src)
			_, err = sendSock.WriteToIP(respPacket, &net.IPAddr{IP: packet.Header.Src})
			log.Printf("TCP response error: %v", err)
		}
	}

}
