// package logic

// import (
// 	"bytes"
// 	"fmt"
// 	"io"
// 	"net/url"
// 	"sync"
// 	"time"

// 	"github.com/gorilla/websocket"
// 	"github.com/q191201771/lal/pkg/httpflv"
// 	"github.com/q191201771/lal/pkg/remux"
// )

// type WsFlvPullSession struct {
// 	conn *websocket.Conn

// 	appName    string
// 	streamName string

// 	group *Group
// 	cps   ICustomizePubSessionContext

// 	lastUrl string

// 	mu      sync.Mutex
// 	running bool

// 	parserBuf bytes.Buffer
// }

// func NewWsFlvPullSession(appName, streamName string, group *Group, cps ICustomizePubSessionContext) *WsFlvPullSession {

// 	return &WsFlvPullSession{
// 		appName:    appName,
// 		streamName: streamName,
// 		group:      group,
// 		cps:        cps,
// 	}
// }

// func (s *WsFlvPullSession) Start(rawUrl string) error {

// 	u, err := url.Parse(rawUrl)
// 	if err != nil {
// 		return err
// 	}

// 	fmt.Printf("wsflv connecting host=%s path=%s\n", u.Host, u.Path)

// 	s.lastUrl = rawUrl

// 	s.mu.Lock()
// 	s.running = true
// 	s.mu.Unlock()

// 	go s.runLoop()

// 	return nil
// }

// func (s *WsFlvPullSession) runLoop() {

// 	for {

// 		if !s.isRunning() {
// 			return
// 		}

// 		err := s.connectAndRead()

// 		if !s.isRunning() {
// 			return
// 		}

// 		fmt.Printf("wsflv reconnect after error: %v\n", err)

// 		time.Sleep(3 * time.Second)
// 	}
// }

// func (s *WsFlvPullSession) connectAndRead() error {

// 	conn, _, err := websocket.DefaultDialer.Dial(s.lastUrl, nil)
// 	if err != nil {
// 		return err
// 	}

// 	defer conn.Close()

// 	s.conn = conn
// 	s.parserBuf.Reset()

// 	const flvTagTrailerSize = 4

// 	for {

// 		_, data, err := conn.ReadMessage()
// 		if err != nil {
// 			return err
// 		}

// 		s.parserBuf.Write(data)

// 		for {

// 			tag, err := httpflv.ReadTag(bytes.NewReader(s.parserBuf.Bytes()))

// 			if err == io.EOF {
// 				break
// 			}

// 			if err != nil {
// 				s.parserBuf.Reset()
// 				break
// 			}

// 			msg := remux.FlvTag2RtmpMsg(tag)

// 			if err := s.cps.FeedRtmpMsg(msg); err != nil {
// 				return err
// 			}

// 			totalSize := httpflv.TagHeaderSize + len(tag.Raw) + flvTagTrailerSize

// 			if s.parserBuf.Len() < totalSize {
// 				break
// 			}

// 			s.parserBuf.Next(totalSize)
// 		}
// 	}
// }

// func (s *WsFlvPullSession) Stop() error {

// 	s.mu.Lock()
// 	s.running = false

// 	if s.conn != nil {
// 		_ = s.conn.Close()
// 	}

// 	s.mu.Unlock()

// 	return nil
// }

// func (s *WsFlvPullSession) isRunning() bool {

// 	s.mu.Lock()
// 	defer s.mu.Unlock()

// 	return s.running
// }

package logic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"
)

type WsFlvPullSession struct {
	appName    string
	streamName string

	group *Group
	cps   ICustomizePubSessionContext

	conn   *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	mutex   sync.Mutex
	running bool

	wg sync.WaitGroup
}

func NewWsFlvPullSession(
	app string,
	stream string,
	group *Group,
	cps ICustomizePubSessionContext,
) *WsFlvPullSession {

	ctx, cancel := context.WithCancel(context.Background())

	return &WsFlvPullSession{
		appName:    app,
		streamName: stream,
		group:      group,
		cps:        cps,
		ctx:        ctx,
		cancel:     cancel,
	}
}

func (s *WsFlvPullSession) Start(rawURL string) error {

	s.mutex.Lock()
	if s.running {
		s.mutex.Unlock()
		return errors.New("already running")
	}
	s.running = true
	s.mutex.Unlock()

	s.wg.Add(1)

	go s.loop(rawURL)

	return nil
}

func (s *WsFlvPullSession) Stop() {

	s.cancel()

	s.mutex.Lock()

	if s.conn != nil {
		s.conn.Close()
	}

	s.mutex.Unlock()

	s.wg.Wait()
}

func (s *WsFlvPullSession) loop(rawURL string) {

	defer s.wg.Done()

	retryDelay := 3 * time.Second

	for {

		select {
		case <-s.ctx.Done():
			return
		default:
		}

		err := s.connectAndRead(rawURL)

		if err != nil {
			Log.Warnf("wsflv pull error. stream=%s err=%v", s.streamName, err)
		}

		select {
		case <-s.ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

func (s *WsFlvPullSession) connectAndRead(rawURL string) error {

	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	dialer := websocket.DefaultDialer

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return err
	}

	s.mutex.Lock()
	s.conn = conn
	s.mutex.Unlock()

	defer conn.Close()

	return s.readLoop(conn)
}

func (s *WsFlvPullSession) readLoop(conn *websocket.Conn) error {

	for {

		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		reader := bytes.NewReader(data)

		for {

			tag, err := httpflv.ReadTag(reader)

			if err == io.EOF {
				break
			}

			if err != nil {
				return err
			}

			msg := remux.FlvTag2RtmpMsg(tag)

			err = s.cps.FeedRtmpMsg(msg)
			if err != nil {
				return err
			}
		}
	}
}

type WsMessageReader struct {
	conn *websocket.Conn
	buf  []byte
	pos  int
}

func NewWsMessageReader(conn *websocket.Conn) *WsMessageReader {
	return &WsMessageReader{
		conn: conn,
	}
}

func (r *WsMessageReader) Read(p []byte) (int, error) {

	if r.pos >= len(r.buf) {

		_, msg, err := r.conn.ReadMessage()

		if err != nil {
			return 0, err
		}

		r.buf = msg
		r.pos = 0
	}

	n := copy(p, r.buf[r.pos:])

	r.pos += n

	return n, nil
}
