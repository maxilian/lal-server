package jt1078

import "net"

type Server struct {
	addr     string
	observer IServerObserver
	ln       net.Listener
}

func NewServer(addr string, observer IServerObserver) *Server {
	return &Server{
		addr:     addr,
		observer: observer,
	}
}

func (s *Server) Listen() error {
	var err error
	s.ln, err = net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	Log.Infof("start jt1078 server listen. addr=%s", s.addr)
	return nil
}

func (s *Server) RunLoop() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			Log.Errorf("accept error: %v", err)
			continue
		}
		go s.handleTcpConnect(conn)
	}
}

func (s *Server) handleTcpConnect(conn net.Conn) {
	Log.Infof("accept jt1078 connection. remote=%s", conn.RemoteAddr())

	session := NewServerSession(s, conn)

	// notify new
	if s.observer != nil {
		if err := s.observer.OnNewJt1078PubSession(session); err != nil {
			Log.Errorf("observer reject session: %v", err)
			_ = conn.Close()
			return
		}
	}

	err := session.RunLoop()
	Log.Infof("jt1078 session end. err=%v", err)

	if s.observer != nil {
		s.observer.OnDelJt1078PubSession(session)
	}
}
