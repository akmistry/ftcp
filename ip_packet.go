package dtcp

import (
	"fmt"

	"golang.org/x/net/ipv4"
)

type IPPacket struct {
	Header *ipv4.Header
	buf    []byte
}

func MakeIPPacket(buf []byte) (*IPPacket, error) {
	header, err := ipv4.ParseHeader(buf)
	if err != nil {
		return nil, err
	}
	if len(buf) < header.TotalLen {
		return nil, fmt.Errorf("ip_packet: buffer len %d < packet length %d", len(buf), header.TotalLen)
	}
	buf = buf[:header.TotalLen]
	packet := &IPPacket{
		buf:    buf,
		Header: header,
	}
	return packet, nil
}

func (p *IPPacket) String() string {
	return p.Header.String()
}

func (p *IPPacket) Payload() []byte {
	return p.buf[p.Header.Len:]
}

func IPChecksum(packet []byte) uint16 {
	if len(packet) < 20 {
		panic("Invalid packet length")
	}

	var sum tcpChecksum
	sum.AddBuf(packet[0:10])
	sum.AddBuf(packet[12:])
	return sum.Sum()
}

func IPSetChecksum(packet []byte, sum uint16) {
	if len(packet) < 20 {
		panic("Invalid packet length")
	}
	be.PutUint16(packet[10:12], sum)
}
