package dtcp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
)

var (
	be = binary.BigEndian

	errTcpBufTooSmall = errors.New("tcp_header: buffer too small")
)

const (
	flagFin = 1 << 0
	flagSyn = 1 << 1
	flagRst = 1 << 2
	flagPsh = 1 << 3
	flagAck = 1 << 4
	flagUrg = 1 << 5
)

func isFlag(v, f byte) bool {
	return (v & f) == f
}

type TCPTimestamp struct {
	Sender    uint32
	EchoReply uint32
}

func (t *TCPTimestamp) String() string {
	if t == nil {
		return "<empty>"
	}
	return fmt.Sprintf("sender_ts=%d echo_reply_ts=%d", t.Sender, t.EchoReply)
}

type TCPHeader struct {
	SrcPort       uint16
	DstPort       uint16
	SeqNum        uint32
	AckNum        uint32
	DataOff       int
	Fin           bool
	Syn           bool
	Rst           bool
	Psh           bool
	Ack           bool
	Urg           bool
	WindowSize    int
	Checksum      uint16
	UrgentPointer int
	Options       []byte
	Timestamp     *TCPTimestamp
}

func ParseTCPHeader(buf []byte) (*TCPHeader, error) {
	if len(buf) < 20 {
		return nil, errTcpBufTooSmall
	}

	header := &TCPHeader{
		SrcPort:       be.Uint16(buf[0:2]),
		DstPort:       be.Uint16(buf[2:4]),
		SeqNum:        be.Uint32(buf[4:8]),
		AckNum:        be.Uint32(buf[8:12]),
		DataOff:       int(buf[12]>>4) * 4,
		Fin:           isFlag(buf[13], flagFin),
		Syn:           isFlag(buf[13], flagSyn),
		Rst:           isFlag(buf[13], flagRst),
		Psh:           isFlag(buf[13], flagPsh),
		Ack:           isFlag(buf[13], flagAck),
		Urg:           isFlag(buf[13], flagUrg),
		WindowSize:    int(be.Uint16(buf[14:16])),
		Checksum:      be.Uint16(buf[16:18]),
		UrgentPointer: int(be.Uint16(buf[18:20])),
	}
	if len(buf) < header.DataOff {
		return nil, fmt.Errorf("tcp_header: buffer size %d < data offset %d (%d bytes)",
			len(buf), header.DataOff/4, header.DataOff)
	}

	opts := buf[20:header.DataOff]
	for off := 0; off < len(opts); {
		optionKind := opts[off]
		if optionKind == 0 {
			// End of options list
			break
		} else if optionKind == 1 {
			// No-op (padding)
			off++
			continue
		}
		optionLen := opts[off+1]
		switch optionKind {
		case 2:
			// MSS
		case 3:
			// Window scaling
		case 4:
			// SACK permitted
		case 5:
			// SACK
		case 8:
			// Timestamp
			if optionLen != 10 {
				log.Printf("Invalid TCP timestamp length: %d", optionLen)
				break
			}
			header.Timestamp = &TCPTimestamp{
				Sender:    be.Uint32(opts[off+2 : off+6]),
				EchoReply: be.Uint32(opts[off+6 : off+10]),
			}
		default:
			log.Printf("Unrecognised TCP option %d", optionKind)
		}
		off += int(optionLen)
	}
	header.Options = opts

	return header, nil
}

func (h *TCPHeader) String() string {
	return fmt.Sprintf("src_port=%d dst_port=%d seq=%d ack=%d data_offset=%d "+
		"fin=%t syn=%t rst=%t psh=%t ack=%t urg=%t timestamp=(%v)",
		h.SrcPort, h.DstPort,
		h.SeqNum, h.AckNum,
		h.DataOff,
		h.Fin, h.Syn, h.Rst, h.Psh, h.Ack, h.Urg, h.Timestamp)
}
