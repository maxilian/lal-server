package logic

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"
)

type WsFlvPullSession struct {
	conn       *websocket.Conn
	appName    string
	streamName string
	group      *Group
	cps        ICustomizePubSessionContext
	lastUrl    string
}

func NewWsFlvPullSession(appName, streamName string, group *Group, cps ICustomizePubSessionContext) *WsFlvPullSession {
	return &WsFlvPullSession{
		appName:    appName,
		streamName: streamName,
		group:      group,
		cps:        cps,
	}
}

func (s *WsFlvPullSession) Start(rawUrl string) error {
	u, err := url.Parse(rawUrl)
	if err != nil {
		return fmt.Errorf("invalid wsflv url: %w", err)
	}
	s.lastUrl = rawUrl
	fmt.Printf("connecting to wsflv upstream host=%s path=%s\n", u.Host, u.Path)

	// Dial WebSocket with Gorilla
	dialer := websocket.DefaultDialer
	ws, _, err := dialer.Dial(rawUrl, nil)
	if err != nil {
		return fmt.Errorf("wsflv dial failed: %w", err)
	}

	s.conn = ws
	go s.loop()
	return nil
}

func (s *WsFlvPullSession) loop() {
	for {
		// Read a full WebSocket message
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			fmt.Printf("wsflv pull disconnected: %v\n", err)
			// retry after delay
			time.Sleep(3 * time.Second)
			_ = s.Start(s.lastUrl)
			return
		}

		// Parse FLV tags from the message payload
		r := bytes.NewReader(data)
		for {
			tag, err := httpflv.ReadTag(r)
			if err == io.EOF {
				// Not enough data left in this message
				break
			}
			if err != nil {
				fmt.Printf("flv tag read error: %v\n", err)
				break
			}
			msg := remux.FlvTag2RtmpMsg(tag)
			_ = s.cps.FeedRtmpMsg(msg)
		}
	}
}

func (s *WsFlvPullSession) Stop() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
