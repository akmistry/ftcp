package ftcp

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/akmistry/ftcp/pb"
)

const (
	syncTimeout = 50 * time.Millisecond
)

var (
	ErrTimeout = errors.New("sync_client: timeout")
)

type activeRequest struct {
	req       *pb.SyncRequest
	reply     *pb.SyncReply
	startTime time.Time
	result    chan error
}

type SyncClient struct {
	ctx    context.Context
	cancel context.CancelFunc

	addr string

	// Cumulative number of dial errors. Used to determine when the server is
	// gone.
	dialErrors int

	requests map[uint64]*activeRequest
	sendCh   chan *activeRequest

	lock sync.Mutex
	cond *sync.Cond
}

func NewSyncClient(addr string) *SyncClient {
	ctx, cf := context.WithCancel(context.TODO())
	c := &SyncClient{
		ctx:      ctx,
		cancel:   cf,
		addr:     addr,
		requests: make(map[uint64]*activeRequest),
		sendCh:   make(chan *activeRequest, 64),
	}
	c.cond = sync.NewCond(&c.lock)

	go c.sender()
	return c
}

func (c *SyncClient) Close() error {
	c.cancel()
	return nil
}

func (c *SyncClient) connectToServer() (net.Conn, error) {
	return net.Dial("tcp", c.addr)
}

func (c *SyncClient) receiver(conn net.Conn) {
	defer conn.Close()
	for {
		reply := pb.SyncReply{}
		_, err := pb.ReadMessage(conn, &reply)
		if err != nil {
			LogWarn("SyncClient: error receiving reply message: %v", err)
			return
		}

		c.lock.Lock()
		ar := c.requests[reply.MsgId]
		if ar != nil {
			*ar.reply = reply
		}
		c.lock.Unlock()
		if ar != nil {
			close(ar.result)
		}
	}
}

func (c *SyncClient) sender() {
	var conn net.Conn
	var err error

	for {
		var ar *activeRequest
		select {
		case <-c.ctx.Done():
			if conn != nil {
				conn.Close()
			}
			return
		case ar = <-c.sendCh:
		}

		if time.Since(ar.startTime) > syncTimeout {
			// Drop this request and let SendSync()'s timeout handle the error.
			continue
		}

		if conn == nil {
			conn, err = c.connectToServer()
			if err != nil {
				LogWarn("SyncClient: error connecting to server %s, %v", c.addr, err)
				// Drop this request and try again to connect.
				continue
			}
			go c.receiver(conn)
		}

		_, err = pb.WriteMessage(conn, ar.req)
		if err != nil {
			LogWarn("SyncClient: error sending request message: %v", err)
			conn.Close()
			conn = nil
		}
	}
}

func (c *SyncClient) SendSync(req *pb.SyncRequest, reply *pb.SyncReply) error {
	ar := &activeRequest{
		req:       req,
		reply:     reply,
		startTime: time.Now(),
		result:    make(chan error, 1),
	}
	var msg_id uint64
	for {
		msg_id = rand.Uint64()
		req.MsgId = msg_id

		c.lock.Lock()
		_, ok := c.requests[msg_id]
		if ok {
			// Retry ID generation
			c.lock.Unlock()
			continue
		}
		c.requests[msg_id] = ar
		c.lock.Unlock()
		break
	}

	defer func() {
		c.lock.Lock()
		delete(c.requests, msg_id)
		c.lock.Unlock()
	}()

	timeoutCh := time.After(syncTimeout)

	select {
	case c.sendCh <- ar:
		select {
		case err := <-ar.result:
			return err
		case <-timeoutCh:
			return ErrTimeout
		}
	case <-timeoutCh:
		return ErrTimeout
	}
}
