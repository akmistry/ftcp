package ftcp

import (
	"sync"

	"github.com/akmistry/go-util/ringbuffer"

	"github.com/akmistry/ftcp/pb"
)

type SyncedBuffer struct {
	// Sequence number of the first byte in the buffer
	startSeq uint64
	// Next sequence number to send
	// This is NOT synchonised with the failover server. It can be re-created
	// on failover by retransmitting starting at |startSeq|.
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

func (b *SyncedBuffer) Free() int {
	return b.Cap() - b.Len()
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
	return b.rb.Fetch(buf, offset)
}

func (b *SyncedBuffer) Consume(n int) {
	if n > b.Len() {
		panic("n > b.Len()")
	}
	b.startSeq += uint64(n)
	b.rb.Consume(n)
}

func (b *SyncedBuffer) FillStateUpdate(update *pb.BufferStateUpdate) {
	update.StartSeq = b.startSeq
	update.BufLen = uint32(b.rb.Len())
}

func (b *SyncedBuffer) UpdateState(req *pb.BufferStateUpdate) {
	if req.StartSeq != 0 {
		if req.StartSeq > b.startSeq {
			// Sender is ahead on ACKs (has received them from the remote end), or
			// recv buffer has been read by the client. Just fast-forward and drop data.
			diff := req.StartSeq - b.startSeq
			b.startSeq = req.StartSeq
			// Drop data from the buffer
			if diff > uint64(b.rb.Len()) {
				diff = uint64(b.rb.Len())
			}
			b.rb.Consume(int(diff))
			if b.nextSeq < b.startSeq {
				b.nextSeq = b.startSeq
			}
		} else {
			// Sender is behind. Our buffer has data ack'd/read, so do nothing.
			// Expect the sender to fast-forward when it gets our state.
		}
	}

	if req.AppendSeq != 0 {
		currEndSeq := b.EndSeq()
		if req.AppendSeq < currEndSeq {
			// Sender is behind us. Drop any data we already have, and append the
			// rest.
			diff := currEndSeq - req.AppendSeq
			if diff < uint64(len(req.AppendData)) {
				n, _ := b.rb.Append(req.AppendData[int(diff):])
				if n < (len(req.AppendData) - int(diff)) {
					// This could happen if the start sequence number has gone out of
					// sync and we're far behind, causing appended data to overflow the
					// ring buffer. Panic for now, since we synchornise the sequence
					// number above, and therefore this represents a programming error.
					// TODO: Make this more robust.
					panic("n < (len(req.AppendData) - int(diff))")
				}
			}
		} else {
			// We're behind. Need to have the sender send us more data. We could
			// probably store the received data and have the sender send us the gap,
			// but that would make the logic more complicated. For now, just drop the
			// received data, and expect the sender to give us everything starting at
			// the end of our buffer.
		}
	}
}

func (b *SyncedBuffer) Sync(req *pb.BufferStateUpdate, reply *pb.BufferStateUpdate) {
	// Assume message ID and key have already been verified.
	// |reply| is assumed to be empty.

	b.UpdateState(req)

	// Always tell the sender what our state is, so it can synchronise.
	b.FillStateUpdate(reply)

	// If we are too far ahead of the sender, send them our data so it can catch
	// up.
	if req.StartSeq != 0 {
		senderSeqEnd := req.StartSeq + uint64(req.BufLen)
		currEndSeq := b.EndSeq()
		if senderSeqEnd < currEndSeq {
			diff := currEndSeq - senderSeqEnd
			if diff > uint64(b.rb.Len()) {
				diff = uint64(b.rb.Len())
			}
			reply.AppendSeq = currEndSeq - diff
			reply.AppendData = make([]byte, int(diff))
			n := b.rb.Fetch(reply.AppendData, b.rb.Len()-int(diff))
			if n != int(diff) {
				panic("n != int(diff)")
			}
		}
	}
}
