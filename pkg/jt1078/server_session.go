package jt1078

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var globalSessionIndex uint64

type ServerSession struct {
	uniqueKey string
	conn      net.Conn
	server    *Server

	closed int32
	mu     sync.Mutex
}

func NewServerSession(server *Server, conn net.Conn) *ServerSession {
	uk := atomic.AddUint64(&globalSessionIndex, 1)

	return &ServerSession{
		uniqueKey: func() string {
			var _ uint64 = uk
			return generateSessionKey()
		}(),
		conn:   conn,
		server: server,
	}
}

func generateSessionKey() string {
	return "JT1078-" + time.Now().Format("20060102150405") + "-" + time.Now().Format("000000")
}

func (s *ServerSession) UniqueKey() string {
	return s.uniqueKey
}

func (s *ServerSession) RunLoop() error {
	defer s.Close()

	buf := make([]byte, 4096)

	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				return err
			}
			return nil
		}

		if n <= 0 {
			continue
		}

		s.handleRawData(buf[:n])
	}
}

func (s *ServerSession) Close() {
	if !atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *ServerSession) handleRawData(data []byte) {
	if len(data) < 12 {
		return
	}

	timestamp := binary.BigEndian.Uint32(data[4:8])
	payload := data[12:]

	if len(payload) == 0 {
		return
	}

	if s.server != nil && s.server.observer != nil {
		s.server.observer.OnJt1078Frame(s, payload, timestamp)
	}
}
