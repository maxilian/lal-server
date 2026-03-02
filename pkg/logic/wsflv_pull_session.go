package logic

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	transcoder "github.com/maxilian/audio-transcoder"
	"github.com/q191201771/lal/pkg/base"
	howen "github.com/q191201771/lal/pkg/howen264"
	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"
)

const (
	aacSamplesPerFrame = 1024
	aacSampleRate      = 8000
)

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
	g711Decoder  *transcoder.G711Decoder
	//g726Decoder       *transcoder.G726Decoder
	aacEncoder        *transcoder.AACEncoder
	aacHeaderSent     bool
	audioNextTS       uint32
	audioClockStarted bool
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
	// initialize audio once
	if err := s.initAudio("pcma"); err != nil {
		return err
	}
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
				var pcm []byte

				// Decode G.711 if available
				if s.g711Decoder != nil {
					pcm = make([]byte, len(frame)*2)
					n, _ := s.g711Decoder.Decode(frame, pcm)
					pcm = pcm[:n]
				} else {
					pcm = frame
				}

				if len(pcm) == 0 {
					continue
				}
				//fmt.Println("AAC ASC:", s.aacEncoder.ExtraData())
				// Send AAC sequence header once
				if !s.aacHeaderSent && len(s.aacEncoder.ExtraData()) > 0 {

					asc := s.aacEncoder.ExtraData()
					payload := buildAACRTMPPayload(asc, true)

					rtmpMsg := base.RtmpMsg{
						Header: base.RtmpHeader{
							Csid:         4,
							MsgLen:       uint32(len(payload)),
							MsgTypeId:    base.RtmpTypeIdAudio,
							MsgStreamId:  1,
							TimestampAbs: uint32(ts),
						},
						Payload: payload,
					}

					if err := s.cps.FeedRtmpMsg(rtmpMsg); err != nil {
						return err
					}

					s.aacHeaderSent = true
				}

				// Encode PCM → AAC
				_, _ = s.aacEncoder.Encode(pcm, func(aacPkt []byte) {

					// Ignore ASC packets
					if len(aacPkt) == 2 &&
						len(s.aacEncoder.ExtraData()) == 2 &&
						aacPkt[0] == s.aacEncoder.ExtraData()[0] &&
						aacPkt[1] == s.aacEncoder.ExtraData()[1] {
						return
					}

					// === Drive timestamps from AAC frame duration ===

					const frameDurationMs = uint32(aacSamplesPerFrame * 1000 / aacSampleRate)
					// = 128ms at 8kHz

					if !s.audioClockStarted {
						s.audioNextTS = uint32(ts) // use first ts once
						s.audioClockStarted = true
					}

					currentTS := s.audioNextTS
					s.audioNextTS += frameDurationMs

					payload := buildAACRTMPPayload(aacPkt, false)

					rtmpMsg := base.RtmpMsg{
						Header: base.RtmpHeader{
							Csid:         4,
							MsgLen:       uint32(len(payload)),
							MsgTypeId:    base.RtmpTypeIdAudio,
							MsgStreamId:  1,
							TimestampAbs: currentTS,
						},
						Payload: payload,
					}

					_ = s.cps.FeedRtmpMsg(rtmpMsg)
				})
			}
			// else if ft == howen.FrameAudio { /* wire audio later */ }

		default:
			// ignore
		}
	}
}

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

func (s *WsFlvPullSession) initAudio(audioCodec string) error {
	// ===== Init G.711 decoder =====
	switch audioCodec {
	case "pcma", "pcmu":
		s.g711Decoder = &transcoder.G711Decoder{}
	default:
		s.g711Decoder = nil
	}

	// ===== Init AAC encoder =====
	s.aacEncoder = &transcoder.AACEncoder{}
	sampleRate := 8000
	channels := 1
	_, err := s.aacEncoder.Create(sampleRate, channels, 0)
	if err != nil {
		return fmt.Errorf("failed to create AAC encoder: %v", err)
	}

	s.aacHeaderSent = false
	s.audioNextTS = 0
	s.audioClockStarted = false
	return nil
}

// buildAACRTMPPayload wraps raw AAC data into an FLV audio tag payload.
// 'isSeqHeader' should be true for the AAC sequence header, false for actual frames.
func buildAACRTMPPayload(aacData []byte, isSeqHeader bool) []byte {

	// First byte: SoundFormat(10=AAC) | SoundRate(3=44k) | SoundSize(1=16bit) | SoundType(1=stereo)
	// For AAC, SoundRate/SoundSize are ignored.
	soundHeader := byte(0xAF)

	// Second byte: AACPacketType
	// 0 = sequence header
	// 1 = raw AAC frame
	var aacPacketType byte
	if isSeqHeader {
		aacPacketType = 0
	} else {
		aacPacketType = 1
	}

	payload := make([]byte, 2+len(aacData))
	payload[0] = soundHeader
	payload[1] = aacPacketType
	copy(payload[2:], aacData)

	return payload
}
