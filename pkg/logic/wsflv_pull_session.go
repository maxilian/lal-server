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
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"
)

type WsFlvPullSession struct {
	appName       string
	streamName    string
	url           string
	conn          *websocket.Conn
	mu            sync.Mutex
	parserBuf     bytes.Buffer
	cps           ICustomizePubSessionContext
	stopped       atomic.Bool
	stats         WsFlvPullStats
	bytesReceived atomic.Uint64
	startTime     time.Time
	stopChan      chan struct{}
	lastStatTime  time.Time
	lastStatBytes uint64
}

type WsFlvPullStats struct {
	Url        string    `json:"url"`
	AppName    string    `json:"app_name"`
	StreamName string    `json:"stream_name"`
	StartTime  time.Time `json:"start_time"`

	BytesReceived uint64  `json:"bytes_received"`
	BitrateKbps   float64 `json:"bitrate_kbps"`

	LastUpdate time.Time `json:"last_update"`
}

func NewWsFlvPullSession(
	appName string,
	streamName string,
	group *Group,
	cps ICustomizePubSessionContext,
) *WsFlvPullSession {

	return &WsFlvPullSession{
		appName:    appName,
		streamName: streamName,
		cps:        cps,
	}
}

// func (s *WsFlvPullSession) Start(url string) error {

// 	s.url = url

// 	for !s.stopped.Load() {

// 		err := s.connectAndRead()

// 		if s.stopped.Load() {
// 			return nil
// 		}

// 		Log.Warnf("wsflv pull error. stream=%s err=%v",
// 			s.streamName, err)

// 		time.Sleep(3 * time.Second)
// 	}

// 	return nil
// }

func (s *WsFlvPullSession) Start(url string) error {

	s.url = url

	now := time.Now()

	s.startTime = now
	s.lastStatTime = now
	s.lastStatBytes = 0

	s.stats = WsFlvPullStats{
		Url:        url,
		AppName:    s.appName,
		StreamName: s.streamName,
		StartTime:  now,
		LastUpdate: now,
	}

	s.stopChan = make(chan struct{})

	go s.updateStatsLoop()

	for !s.stopped.Load() {

		err := s.connectAndRead()

		if s.stopped.Load() {
			return nil
		}

		Log.Warnf("wsflv pull error. stream=%s err=%v",
			s.streamName, err)

		time.Sleep(3 * time.Second)
	}

	return nil
}

func (s *WsFlvPullSession) updateStatsLoop() {

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {

		select {

		case <-ticker.C:

			now := time.Now()

			totalBytes := s.bytesReceived.Load()

			duration := now.Sub(s.startTime).Seconds()

			var bitrate float64

			if duration > 0 {

				bytesDiff := totalBytes - s.lastStatBytes
				timeDiff := now.Sub(s.lastStatTime).Seconds()

				if timeDiff > 0 {
					bitrate = float64(bytesDiff*8) / timeDiff / 1000
				}
			}

			s.mu.Lock()

			s.stats.BytesReceived = totalBytes
			s.stats.BitrateKbps = bitrate
			s.stats.LastUpdate = now

			s.mu.Unlock()

			s.lastStatBytes = totalBytes
			s.lastStatTime = now

		case <-s.stopChan:
			return
		}
	}
}

func (s *WsFlvPullSession) Stop() {

	if s.stopped.CompareAndSwap(false, true) {

		close(s.stopChan)

		s.mu.Lock()

		if s.conn != nil {
			_ = s.conn.Close()
		}

		s.mu.Unlock()
	}
}

func (s *WsFlvPullSession) GetStats() WsFlvPullStats {

	stats := s.stats

	stats.BytesReceived = s.bytesReceived.Load()

	duration := time.Since(s.startTime).Seconds()

	if duration > 0 {
		stats.BitrateKbps =
			float64(stats.BytesReceived*8) / duration / 1000
	}

	stats.LastUpdate = time.Now()

	return stats
}

func (s *WsFlvPullSession) connectAndRead() error {

	dialer := websocket.DefaultDialer

	conn, _, err := dialer.Dial(s.url, nil)
	if err != nil {
		return err
	}

	Log.Infof("wsflv connected. stream=%s url=%s",
		s.streamName, s.url)

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
			s.conn = nil
		}
		s.mu.Unlock()
	}()

	s.parserBuf.Reset()

	flvHeaderSkipped := false

	for {

		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		s.bytesReceived.Add(uint64(len(data)))

		s.parserBuf.Write(data)

		// Skip FLV header once
		if !flvHeaderSkipped {

			if s.parserBuf.Len() < 13 {
				continue
			}

			header := s.parserBuf.Next(13)

			if string(header[:3]) != "FLV" {
				Log.Errorf("invalid flv header")
				return io.ErrUnexpectedEOF
			}

			flvHeaderSkipped = true

			Log.Infof("wsflv header skipped. stream=%s", s.streamName)
		}

		for {

			reader := bytes.NewReader(s.parserBuf.Bytes())

			tag, err := httpflv.ReadTag(reader)

			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}

			if err != nil {

				Log.Warnf("flv parse error: %v", err)

				s.parserBuf.Reset()
				flvHeaderSkipped = false

				break
			}

			rtmpMsg := remux.FlvTag2RtmpMsg(tag)

			err = s.cps.FeedRtmpMsg(rtmpMsg)
			if err != nil {
				return err
			}

			consumed := len(s.parserBuf.Bytes()) - reader.Len()

			if consumed <= 0 {
				break
			}

			s.parserBuf.Next(consumed)
		}
	}
}
