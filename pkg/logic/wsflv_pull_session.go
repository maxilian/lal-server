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

 "encoding/base64"
  "fmt"

   // "github.com/q191201771/lal/pkg/aac"

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
	aud audioState
	
audSeen         int
    vidSeen         int
    audNonAac       int
    audRawBeforeASC int

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

func (s *WsFlvPullSession) Start(url string, wsHeaders map[string]string) error {

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

	for !s.stopped.Load() {

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

	audSeen, vidSeen := 0, 0
    audNonAac, audRawBeforeASC := 0, 0

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
						
				
				s.vidSeen++
				if s.vidSeen%120 == 0 {
					Log.Infof("[TAP] video=%d audio=%d", s.vidSeen, s.audSeen)
				}
			
			case howen.FrameAudio:
				// Count every audio frame observed (regardless of codec)
				s.audSeen++

				// Each Howen audio frame: frame[0]=codec, frame[1]=AAC subtype (if AAC), frame[2:] = payload
				if len(frame) < 2 {
					// Too small to contain codec + subtype; ignore but DO NOT return
					if s.audSeen <= 3 {
						Log.Warnf("[AUDIO] tiny frame len=%d ts=%d (ignored)", len(frame), ts)
					}
					break
				}

				codec   := frame[0] // your mapping: 0x00 => AAC (adjust if your firmware differs)
				aacType := frame[1] // for AAC only: 0x00 => ASC, 0x01 => RAW AAC
				data    := frame[2:]

				// 1) Accept only AAC in the FLV path for flv.js; drop other codecs (G.711/ADPCM/etc)
				if codec != 0x00 {
					s.audNonAac++
					if s.audNonAac <= 5 {
						Log.Warnf("[AUDIO] non-AAC codec=0x%02X len=%d ts=%d (dropped; flv.js expects AAC)", codec, len(frame), ts)
					}
					break // keep connection alive, just don't feed non-AAC
				}

				// 2) Handle AAC subtypes
				switch aacType {
				case 0x00: // AAC Sequence Header (AudioSpecificConfig)
					// Store ASC exactly as provided by device (no synth unless device never sends it)
					s.aud.asc = append(s.aud.asc[:0], data...)

					// Derive channel count from ASC (MPEG-4 AudioSpecificConfig):
					// channel_configuration is 4 bits in the high nibble of byte 1 (after 5+4 bits)
					s.aud.ch = 1 // default mono if unknown
					if len(data) >= 2 {
						ch := int((data[1] & 0x78) >> 3) // upper 4 bits of second ASC byte
						if ch == 1 || ch == 2 {
							s.aud.ch = ch
						}
					}

					// Send FLV AAC sequence header once
					if !s.aud.sentAACSeq {
						if err := s.pushAACSeq(ts); err != nil {
							return err
						}
						s.aud.sentAACSeq = true
						Log.Infof("[AUDIO] AAC ASC received & sent (ch=%d bytes=%d ts=%d)", s.aud.ch, len(data), ts)
					}

				case 0x01: // Raw AAC frame
					if !s.aud.sentAACSeq {
						// We haven't seen ASC yet: skip raw but DO NOT return (keeps video flowing)
						s.audRawBeforeASC++
						if s.audRawBeforeASC <= 5 {
							Log.Warnf("[AUDIO] raw AAC before ASC; skipping (len=%d ts=%d)", len(data), ts)
						}
						break
					}
					// ASC already sent → forward raw AAC
					if err := s.pushAACRaw(ts, data); err != nil {
						return err
					}

				default:
					// Unknown AAC subtype; ignore safely
					if s.audSeen <= 5 {
						Log.Warnf("[AUDIO] unknown AAC subtype=0x%02X len=%d ts=%d (ignored)", aacType, len(data), ts)
					}
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
	
if len(b) >= 12 && b[0] == 8 { // TagType=8 (audio)
    ts := (uint32(b[4])<<16) | (uint32(b[5])<<8) | uint32(b[6])
    ts |= uint32(b[7]) << 24
    dumpTagAsB64(ts, b)
  }

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

// --- static AAC config for quick testing ---
const forcedSampleHz = 8000 // try 16000 first; if silent, try 8000
const forcedChannels = 1     // 1 = mono, 2 = stereo

type audioState struct {
    sentAACSeq bool
    asc        []byte
    ch         int
}

// Map Hz -> MPEG-4 ASC sampling index
func aacSamplingIdx(hz int) (uint8, bool) {
    switch hz {
    case 96000: return 0, true
    case 88200: return 1, true
    case 64000: return 2, true
    case 48000: return 3, true
    case 44100: return 4, true
    case 32000: return 5, true
    case 24000: return 6, true
    case 22050: return 7, true
    case 16000: return 8, true
    case 12000: return 9, true
    case 11025: return 10, true
    case 8000:  return 11, true
    case 7350:  return 12, true
    }
    return 0, false
}

// Build full FLV tag (TagHeader + Payload + PrevTagSize). tagType: 8=audio, 9=video
func packFlvTag(tagType byte, ts uint32, payload []byte) []byte {
    dataSize := len(payload)
    tag := make([]byte, 11+dataSize+4)

    tag[0] = tagType
    tag[1] = byte((dataSize >> 16) & 0xFF)
    tag[2] = byte((dataSize >> 8) & 0xFF)
    tag[3] = byte(dataSize & 0xFF)
    tag[4] = byte((ts >> 16) & 0xFF)
    tag[5] = byte((ts >> 8) & 0xFF)
    tag[6] = byte(ts & 0xFF)
    tag[7] = byte((ts >> 24) & 0xFF)
    // [8..10] StreamID=0
    copy(tag[11:], payload)
    prev := 11 + dataSize
    off := 11 + dataSize
    tag[off+0] = byte((prev >> 24) & 0xFF)
    tag[off+1] = byte((prev >> 16) & 0xFF)
    tag[off+2] = byte((prev >> 8) & 0xFF)
    tag[off+3] = byte(prev & 0xFF)
    return tag
}

// Choose the FLV audio header first byte based on channel count:
// 0xAE = AAC + mono; 0xAF = AAC + stereo
func (s *WsFlvPullSession) makeAudioHdrByte() byte {
    if s.aud.ch == 2 { return 0xAF }
    return 0xAE
}

// Send the AAC Sequence Header (ASC) as one FLV audio tag
func (s *WsFlvPullSession) pushAACSeq(ts uint32) error {
    b0 := s.makeAudioHdrByte()
    payload := make([]byte, 2+len(s.aud.asc))
    payload[0] = b0
    payload[1] = 0x00 // AACPacketType=0 (sequence header)
    copy(payload[2:], s.aud.asc)
    return s.feedOneFlvTag(packFlvTag(8, ts, payload))
}

// Send a raw AAC frame as one FLV audio tag
func (s *WsFlvPullSession) pushAACRaw(ts uint32, raw []byte) error {
    b0 := s.makeAudioHdrByte()
    payload := make([]byte, 2+len(raw))
    payload[0] = b0
    payload[1] = 0x01 // AACPacketType=1 (raw)
    copy(payload[2:], raw)
    return s.feedOneFlvTag(packFlvTag(8, ts, payload))
}



var dumpCnt int

func dumpTagAsB64(ts uint32, fullFlvTag []byte) {
  if dumpCnt >= 6 { return } // 1 seq + 5 raw is plenty
  b64 := base64.StdEncoding.EncodeToString(fullFlvTag)
  fmt.Printf("{\"ts\":%d,\"len\":%d,\"tag_b64\":\"%s\"}\n", ts, len(fullFlvTag), b64)
  dumpCnt++
}
