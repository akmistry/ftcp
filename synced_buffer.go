package dtcp

import (
	"sync"

	"github.com/akmistry/go-util/ringbuffer"
)

type SyncedBuffer struct {
	// Sequence number of the first byte in the buffer
	startSeq uint64
	// Next sequence number to send
	nextSeq uint64
	rb      *ringbuffer.RingBuffer

	lock sync.Mutex
}

func NewSyncedBuffer(initSeq uint32, initWindowSize int) *SyncedBuffer {
	b := &SyncedBuffer{
		startSeq: uint64(initSeq),
		nextSeq:  uint64(initSeq),
		rb:       ringbuffer.NewRingBuffer(make([]byte, initWindowSize)),
	}
	return b
}

func (b *SyncedBuffer) StartSeq() uint64 {
	return b.startSeq
}

// Sequence number of one past the end of the buffer
func (b *SyncedBuffer) EndSeq() uint64 {
	return b.startSeq + uint64(b.rb.Len())
}

func (b *SyncedBuffer) NextSendSeq() uint64 {
	return b.nextSeq
}

func (b *SyncedBuffer) ResetNextSeq() {
	b.nextSeq = b.startSeq
}

func (b *SyncedBuffer) HasUnsentData() bool {
	return b.nextSeq < b.EndSeq()
}

func (b *SyncedBuffer) Len() int {
	return b.rb.Len()
}

func (b *SyncedBuffer) Cap() int {
	return b.rb.Cap()
}

func (b *SyncedBuffer) Ack(ack uint32) bool {
	diff := ack - uint32(b.startSeq)
	sentBytes := uint32(b.nextSeq - b.startSeq)
	if diff > sentBytes {
		LogDebug("ACK %d out of sent range [%d, %d) (buf end: %d)", ack, b.startSeq, b.nextSeq, b.EndSeq())
		return false
	}
	b.startSeq += uint64(diff)
	b.rb.Consume(int(diff))
	return diff > 0
}

func (b *SyncedBuffer) SendData(numBytes int) {
	b.nextSeq += uint64(numBytes)
	if b.nextSeq > b.EndSeq() {
		panic("b.nextSeq > b.EndSeq()")
	}
}

func (b *SyncedBuffer) Append(buf []byte) int {
	n, err := b.rb.Append(buf)
	if err != nil && err != ringbuffer.ErrBufferFull {
		// This should never happen.
		panic(err)
	}
	return n
}

func (b *SyncedBuffer) Fetch(buf []byte, offset int) int {
	if offset < 0 {
		panic("offset < 0")
	}
	data := b.rb.Peek(offset)
	return copy(buf, data)
}
