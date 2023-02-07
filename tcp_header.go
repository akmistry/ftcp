package dtcp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
)

var (
	be = binary.BigEndian

	errTcpBufTooSmall = errors.New("tcp_header: buffer too small")
)

const (
	tcpFlagFin = 1 << 0
	tcpFlagSyn = 1 << 1
	tcpFlagRst = 1 << 2
	tcpFlagPsh = 1 << 3
	tcpFlagAck = 1 << 4
	tcpFlagUrg = 1 << 5

	TcpOptionMss           = 2
	TcpOptionWindowScaling = 3
	TcpOptionSackPermitted = 4
	TcpOptionSack          = 5
	TcpOptionTimestamp     = 8
)

func isFlag(v, f byte) bool {
	return (v & f) == f
}

func toFlag(v bool, f byte) byte {
	if v {
		return f
	}
	return 0
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
		Fin:           isFlag(buf[13], tcpFlagFin),
		Syn:           isFlag(buf[13], tcpFlagSyn),
		Rst:           isFlag(buf[13], tcpFlagRst),
		Psh:           isFlag(buf[13], tcpFlagPsh),
		Ack:           isFlag(buf[13], tcpFlagAck),
		Urg:           isFlag(buf[13], tcpFlagUrg),
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
		case TcpOptionMss:
		case TcpOptionWindowScaling:
		case TcpOptionSackPermitted:
		case TcpOptionSack:
		case TcpOptionTimestamp:
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
		"fin=%t syn=%t rst=%t psh=%t ack=%t urg=%t window_size=%d timestamp=(%v) "+
		"checksum=0x%04x",
		h.SrcPort, h.DstPort,
		h.SeqNum, h.AckNum,
		h.DataOff,
		h.Fin, h.Syn, h.Rst, h.Psh, h.Ack, h.Urg,
		h.WindowSize,
		h.Timestamp, h.Checksum)
}

func (h *TCPHeader) MarshalSize() int {
	// TODO: Support TCP options.
	return 20
}

func (h *TCPHeader) MarshalAppend(buf []byte) ([]byte, error) {
	var tcpHead [20]byte
	be.PutUint16(tcpHead[0:2], h.SrcPort)
	be.PutUint16(tcpHead[2:4], h.DstPort)
	be.PutUint32(tcpHead[4:8], h.SeqNum)
	be.PutUint32(tcpHead[8:12], h.AckNum)
	tcpHead[12] = (20 /* TODO: Replace with header size including options */ / 4) << 4
	flags := toFlag(h.Fin, tcpFlagFin) |
		toFlag(h.Syn, tcpFlagSyn) |
		toFlag(h.Rst, tcpFlagRst) |
		toFlag(h.Psh, tcpFlagPsh) |
		toFlag(h.Ack, tcpFlagAck) |
		toFlag(h.Urg, tcpFlagUrg)
	tcpHead[13] = flags
	// TODO: Support window scaling
	be.PutUint16(tcpHead[14:16], uint16(h.WindowSize))
	// UrgentPointer = 0

	return append(buf, tcpHead[:]...), nil
}

type tcpChecksum struct {
	sum uint32
}

func (c *tcpChecksum) AddBuf(buf []byte) {
	for i := 0; i < len(buf); i += 2 {
		if i+1 >= len(buf) {
			c.sum += uint32(buf[i]) << 8
		} else {
			c.sum += uint32(be.Uint16(buf[i : i+2]))
		}
	}
}

func (c *tcpChecksum) Sum() uint16 {
	return ^(uint16(c.sum) + uint16(c.sum>>16))
}

func TCPChecksum(packet []byte, srcAddr, dstAddr net.IP) uint16 {
	if len(packet) < 20 {
		panic("Invalid packet length")
	}

	srcAddr = srcAddr.To4()
	dstAddr = dstAddr.To4()

	var pseudoHeader [12]byte
	pseudoHeader[0] = srcAddr[0]
	pseudoHeader[1] = srcAddr[1]
	pseudoHeader[2] = srcAddr[2]
	pseudoHeader[3] = srcAddr[3]
	pseudoHeader[4] = dstAddr[0]
	pseudoHeader[5] = dstAddr[1]
	pseudoHeader[6] = dstAddr[2]
	pseudoHeader[7] = dstAddr[3]
	pseudoHeader[8] = 0
	pseudoHeader[9] = 6
	be.PutUint16(pseudoHeader[10:12], uint16(len(packet)))

	var sum tcpChecksum
	sum.AddBuf(pseudoHeader[:])
	sum.AddBuf(packet[0:16])
	sum.AddBuf(packet[18:])

	return sum.Sum()
}

func TCPSetChecksum(packet []byte, sum uint16) {
	if len(packet) < 20 {
		panic("Invalid packet length")
	}
	be.PutUint16(packet[16:18], sum)
}
