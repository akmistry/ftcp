package ftcp

import (
	"net"

	"github.com/akmistry/ftcp/pb"
)

type SyncHandler interface {
	Sync(*pb.SyncRequest, *pb.SyncReply) error
}

type SyncServer struct {
	l net.Listener
	h SyncHandler
}

func NewSyncServer(l net.Listener, h SyncHandler) *SyncServer {
	s := &SyncServer{
		l: l,
		h: h,
	}
	go s.doListener()
	return s
}

func (s *SyncServer) doListener() {
	for {
		conn, err := s.l.Accept()
		if err != nil {
			panic(err)
		}
		go s.doConn(conn)
	}
}

func (s *SyncServer) doConn(conn net.Conn) {
	defer conn.Close()

	for {
		req := pb.SyncRequest{}
		reply := pb.SyncReply{}
		_, err := pb.ReadMessage(conn, &req)
		if err != nil {
			LogWarn("SyncServer: error receiving request message: %v", err)
			return
		}

		err = s.h.Sync(&req, &reply)
		if err != nil {
			LogInfo("SyncServer: error doing sync: %v", err)
			// Ignore this error
			continue
		}

		reply.MsgId = req.MsgId
		_, err = pb.WriteMessage(conn, &reply)
		if err != nil {
			LogWarn("SyncServer: error sending reply message: %v", err)
			return
		}
	}
}
