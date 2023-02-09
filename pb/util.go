package pb

import (
	"encoding/binary"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
)

const (
	MaxMsgLen = 1024 * 1024
)

var (
	marshalOpts = proto.MarshalOptions{
		UseCachedSize: true,
	}

	unmarshalOpts = proto.UnmarshalOptions{
		Merge:          true,
		DiscardUnknown: true,
	}
)

func ReadMessage(r io.Reader, msg proto.Message) (int, error) {
	var msgLenBuf [4]byte
	n, err := io.ReadFull(r, msgLenBuf[:])
	if err != nil {
		return n, err
	}

	msgLen := binary.LittleEndian.Uint32(msgLenBuf[:])
	if msgLen > MaxMsgLen {
		return n, fmt.Errorf("message length too big: %d", msgLen)
	}

	msgBuf := make([]byte, int(msgLen))
	mn, err := io.ReadFull(r, msgBuf)
	n += mn
	if err != nil {
		return n, err
	}
	return n, unmarshalOpts.Unmarshal(msgBuf, msg)
}

func WriteMessage(w io.Writer, msg proto.Message) (int, error) {
	size := marshalOpts.Size(msg)
	if size < 0 {
		panic("size < 0")
	} else if size > MaxMsgLen {
		return 0, fmt.Errorf("message length too big: %d", size)
	}

	msgBuf := make([]byte, 4, 4+size)
	binary.LittleEndian.PutUint32(msgBuf, uint32(size))
	msgBuf, err := marshalOpts.MarshalAppend(msgBuf, msg)
	if err != nil {
		// Don't expect this to ever fail
		panic(err)
	}
	return w.Write(msgBuf)
}
