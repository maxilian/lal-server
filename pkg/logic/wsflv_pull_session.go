package logic

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/q191201771/lal/pkg/base"
	howen "github.com/q191201771/lal/pkg/howen264"
	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"

    "github.com/q191201771/lal/pkg/aac"

)


type audioState struct {
    sentAACSeq bool
}

type WsFlvPullSession struct {
	appName       string
	streamName    string
	url           string
	wsHeaders     http.Header
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

	howenEnabled bool
	howenFrames  bool
	howenJSON    string
	remuxer      *howen.AVCRemuxer
	aud audioState
}

type WsFlvPullStats struct {
	Url        string    `json:"url"`
	AppName    string    `json:"app_name"`
	StreamName string    `json:"stream_name"`
	StartTime  time.Time `json:"start_time"`

	BytesReceived uint64  `json:"bytes_received"`
	BitrateKbps   float64 `json:"bitrate_kbps"`

	LastUpdate time.Time      `json:"last_update"`
	Subs       []base.StatSub `json:"subs"`
}

func NewWsFlvPullSession(appName string, streamName string, group *Group, cps ICustomizePubSessionContext) *WsFlvPullSession {

	return &WsFlvPullSession{
		appName:    appName,
		streamName: streamName,
		cps:        cps,
	}
}

func (s *WsFlvPullSession) Start(url string, wsHeaders map[string]string, cfg WsFlvPullConfig) error {

	httpHeaders := http.Header{}

	for key, value := range wsHeaders {
		httpHeaders.Add(key, value)
	}

	s.wsHeaders = httpHeaders
	s.url = url

	if v := wsHeaders["X-Howen-Mode"]; v != "" {
		if v == "1" {
			s.howenEnabled = true
		}
		if v == "frames" {
			s.howenEnabled, s.howenFrames = true, true
		}
	}
	if j := wsHeaders["X-Howen-Json"]; j != "" {
		s.howenJSON = j

		//fmt.Println(j)
	}

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

	retries := 0
	for !s.stopped.Load() && retries < cfg.MaxRetries {

		var err error

		if s.howenFrames {
			err = s.connectAndReadHowen()
		} else {
			err = s.connectAndRead()
		}

		//err := s.connectAndRead()

		if s.stopped.Load() {
			return nil
		}

		Log.Errorf("wsflv pull error. app=%s stream=%s err=%v",
			s.appName, s.streamName, err)

		//time.Sleep(3 * time.Second)
		time.Sleep(time.Duration(cfg.RemotePullTimeoutSec) * time.Second)
		retries++

	}

	if retries >= cfg.MaxRetries {
		Log.Errorf("wsflv pull failed after %v retries", cfg.MaxRetries)
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

func (s *WsFlvPullSession) connectAndRead() error {

	dialer := websocket.DefaultDialer

	conn, _, err := dialer.Dial(s.url, s.wsHeaders)
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

func (s *WsFlvPullSession) GetStats(group *Group) WsFlvPullStats {

	stats := s.stats

	stats.BytesReceived = s.bytesReceived.Load()

	duration := time.Since(s.startTime).Seconds()

	if duration > 0 {
		stats.BitrateKbps =
			float64(stats.BytesReceived*8) / duration / 1000
	}

	stats.LastUpdate = time.Now()

	if group != nil {
		statGroup := group.GetStat(1000)
		stats.Subs = statGroup.StatSubs
	}

	return stats
}

func (s *WsFlvPullSession) connectAndReadHowen() error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(s.url, s.wsHeaders)
	if err != nil {
		return err
	}
	Log.Infof("howen ws connected. stream=%s url=%s", s.streamName, s.url)

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

	s.remuxer = howen.NewAVCRemuxer()
	s.aud = audioState{}
	// Send Howen control json (action=2) if requested
	if s.howenEnabled && s.howenJSON != "" {
		payload := howen.BuildHowenControlEnvelope([]byte(s.howenJSON))
		if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
			Log.Errorf("send Howen control payload failed: %v", err)
			return err
		}
		Log.Infof("Howen control payload sent. stream=%s", s.streamName)
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		s.bytesReceived.Add(uint64(len(data)))

		action, payload, ok := howen.ParseHowenEnvelope(data)
		if !ok {
			continue
		}

		switch action {
		case howen.ActionJSON:
			// optional: parse zero-terminated json ack if you want (not required)
			continue

		case howen.ActionMedia:
			ft, ts, frame, ok := howen.ParseHowenMedia(payload)
			if !ok {
				continue
			}
			// Video only for now (H.264)
			// if ft == howen.FrameI || ft == howen.FrameP {
			// 	tags, err := s.remuxer.RemuxH264(ts, frame, ft == howen.FrameI)
			// 	if err != nil {
			// 		return err
			// 	}
			// 	for _, t := range tags {
			// 		if err := s.feedOneFlvTag(t); err != nil {
			// 			return err
			// 		}
			// 	}
			// }

			switch ft { 
			case howen.FrameI, howen.FrameP:
				tags, err := s.remuxer.RemuxH264(ts, frame, ft == howen.FrameI)
				if err != nil {
					return err
				}
				for _, t := range tags {
					if err := s.feedOneFlvTag(t); err != nil {
						return err
					}
				}
			case howen.FrameAudio:
				if len(frame) < 2 {
					continue
				}

				codec := frame[0]
				aacType := frame[1]
				payload := frame[2:]

				// Vendor hint: 0x00 => AAC; 0x01/0x02 might be G.711 variants on some devices.
				if codec != 0x00 {
					// Not AAC. flv.js won’t play G.711/PCM in FLV. You can wrap them,
					// but the browser will be silent.
					// Log once if needed, then continue:
					// Log.Debugf("Howen audio: non-AAC codec=0x%02X len=%d", codec, len(payload))
					continue
				}

				switch aacType {
				case 0x00:
					// Sequence header (ASC)
					if len(payload) < 2 {
						// invalid ASC
						break
					}
					// Build FLV AAC sequence header payload: 0xAF 0x00 + ASC
					seqPayload := make([]byte, 2+len(payload))
					seqPayload[0] = 0xAF // SoundFormat=10(AAC), SoundRate=3, SoundSize=1, SoundType=1 (canonical for AAC)
					seqPayload[1] = 0x00 // AACPacketType=0 => sequence header
					copy(seqPayload[2:], payload)

					seqTag := packFlvTag(8, ts, seqPayload)
					if err := s.feedOneFlvTag(seqTag); err != nil {
						return err
					}
					s.aud.sentAACSeq = true
					// Log.Infof("Howen audio: AAC ASC sent, len=%d", len(payload))

				case 0x01:
					// Raw AAC frame
					if !s.aud.sentAACSeq {
						// We *must* have sent ASC before any raw frames for flv.js to work.
						// Drop until we receive ASC from device (aacType=0). Do not synthesize unless you are 100% sure of SR/ch.
						// Log.Warnf("Howen audio: got raw AAC before ASC; dropping %d bytes", len(payload))
						break
					}
					rawPayload := make([]byte, 2+len(payload))
					rawPayload[0] = 0xAF // AAC
					rawPayload[1] = 0x01 // AACPacketType=1 => raw
					copy(rawPayload[2:], payload)

					rawTag := packFlvTag(8, ts, rawPayload)
					if err := s.feedOneFlvTag(rawTag); err != nil {
						return err
					}

				default:
					// Unknown aacType – ignore
					// Log.Warnf("Howen audio: unknown aacType=0x%02X len=%d", aacType, len(payload))
				}

			}
			// else if ft == howen.FrameAudio { /* wire audio later */ }

		default:
			// ignore
		}
	}
}

// helper: parse a single FLV tag (with PrevTagSize) and feed RTMP
// func (s *WsFlvPullSession) feedOneFlvTag(b []byte) error {
// 	reader := bytes.NewReader(b)
// 	tag, err := httpflv.ReadTag(reader)
// 	if err != nil {
// 		return err
// 	}
// 	rtmpMsg := remux.FlvTag2RtmpMsg(tag)
// 	return s.cps.FeedRtmpMsg(rtmpMsg)
// }

func (s *WsFlvPullSession) feedOneFlvTag(b []byte) error {
	reader := bytes.NewReader(b)
	tag, err := httpflv.ReadTag(reader)
	if err != nil {
		//Log.Warnf("feedOneFlvTag: failed to parse FLV tag, err=%v", err)
		return err
	}

	//Log.Infof("feedOneFlvTag: parsed tag type=%d, dataSize=%d, ts=%d", tag.Header.Type, tag.Header.DataSize, tag.Header.Timestamp)

	rtmpMsg := remux.FlvTag2RtmpMsg(tag)
	//Log.Infof("feedOneFlvTag: converted to RTMP msg type=%d, ts=%d, payload=%d bytes", rtmpMsg.Header.MsgTypeId, rtmpMsg.Header.TimestampAbs, len(rtmpMsg.Payload))

	err = s.cps.FeedRtmpMsg(rtmpMsg)
	if err != nil {
		//Log.Warnf("feedOneFlvTag: FeedRtmpMsg failed: %v", err)
		return err
	}

	//Log.Infof("feedOneFlvTag: RTMP msg successfully fed into publisher")
	return nil
}

// packFlvTag builds a full FLV tag with given type (8=audio, 9=video), timestamp (ms), and payload.
// Payload for audio must already start with the FLV audio header byte(s), e.g. 0xAF 0x00 + ASC, or 0xAF 0x01 + raw.
func packFlvTag(tagType byte, ts uint32, payload []byte) []byte {
    dataSize := len(payload)
    tag := make([]byte, 11+dataSize+4)

    // Tag header
    tag[0] = tagType
    tag[1] = byte((dataSize >> 16) & 0xFF)
    tag[2] = byte((dataSize >> 8) & 0xFF)
    tag[3] = byte(dataSize & 0xFF)
    tag[4] = byte((ts >> 16) & 0xFF)
    tag[5] = byte((ts >> 8) & 0xFF)
    tag[6] = byte(ts & 0xFF)
    tag[7] = byte((ts >> 24) & 0xFF)
    // tag[8..10] = 0 (StreamID)

    copy(tag[11:], payload)

    prev := 11 + dataSize
    off := 11 + dataSize
    tag[off+0] = byte((prev >> 24) & 0xFF)
    tag[off+1] = byte((prev >> 16) & 0xFF)
    tag[off+2] = byte((prev >> 8) & 0xFF)
    tag[off+3] = byte(prev & 0xFF)

    return tag
}