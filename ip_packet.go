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
