package jt1078

type IServerObserver interface {
	OnNewJt1078PubSession(session *ServerSession) error
	OnDelJt1078PubSession(session *ServerSession)
	OnJt1078Frame(session *ServerSession, payload []byte, timestamp uint32)
}
