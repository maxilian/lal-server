// Copyright 2022, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package logic

import (
	"fmt"
	"math"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/remux"
	"github.com/q191201771/naza/pkg/bininfo"
)

// server_manager__api.go
//
// 支持http-api功能的部分
//

func (sm *ServerManager) StatLalInfo() base.LalInfo {
	var lalInfo base.LalInfo
	lalInfo.BinInfo = bininfo.StringifySingleLine()
	lalInfo.LalVersion = base.LalVersion
	lalInfo.ApiVersion = base.HttpApiVersion
	lalInfo.NotifyVersion = base.HttpNotifyVersion
	lalInfo.WebUiVersion = base.HttpWebUiVersion
	lalInfo.StartTime = sm.serverStartTime
	lalInfo.ServerId = sm.config.ServerId
	return lalInfo
}

func (sm *ServerManager) StatAllGroup() (sgs []base.StatGroup) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.groupManager.Iterate(func(group *Group) bool {
		sgs = append(sgs, group.GetStat(math.MaxInt32))
		return true
	})
	return
}

func (sm *ServerManager) StatGroup(streamName string) *base.StatGroup {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	g := sm.getGroup("", streamName)
	if g == nil {
		return nil
	}
	// copy
	ret := g.GetStat(math.MaxInt32)
	return &ret
}

func (sm *ServerManager) CtrlStartRelayPull(info base.ApiCtrlStartRelayPullReq) (ret base.ApiCtrlStartRelayPullResp) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	streamName := info.StreamName
	if streamName == "" {
		ctx, err := base.ParseUrl(info.Url, -1)
		if err != nil {
			ret.ErrorCode = base.ErrorCodeStartRelayPullFail
			ret.Desp = err.Error()
			return
		}
		streamName = ctx.LastItemOfPath
	}

	//adding app_name to rename the stream url
	appName := info.AppName
	if appName == "" {
		ctx, err := base.ParseUrl(info.Url, -1)
		if err != nil {
			ret.ErrorCode = base.ErrorCodeStartRelayPullFail
			ret.Desp = err.Error()
			return
		}
		appName = ctx.PathWithoutLastItem
	}

	// 注意，如果group不存在，我们依然relay pull
	g := sm.getOrCreateGroup(appName, streamName)

	sessionId, err := g.StartPull(info)
	if err != nil {
		ret.ErrorCode = base.ErrorCodeStartRelayPullFail
		ret.Desp = err.Error()
	} else {
		ret.ErrorCode = base.ErrorCodeSucc
		ret.Desp = base.DespSucc
		ret.Data.StreamName = streamName
		ret.Data.AppName = appName
		ret.Data.SessionId = sessionId
	}
	return
}

// CtrlStopRelayPull
//
// TODO(chef): 整理错误值
func (sm *ServerManager) CtrlStopRelayPull(streamName string) (ret base.ApiCtrlStopRelayPullResp) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	g := sm.getGroup("", streamName)
	if g == nil {
		ret.ErrorCode = base.ErrorCodeGroupNotFound
		ret.Desp = base.DespGroupNotFound
		return
	}

	ret.Data.SessionId = g.StopPull()
	if ret.Data.SessionId == "" {
		ret.ErrorCode = base.ErrorCodeSessionNotFound
		ret.Desp = base.DespSessionNotFound
		return
	}

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	return
}

// CtrlKickSession
//
// TODO(chef): refactor 不要返回http结果，返回error吧
func (sm *ServerManager) CtrlKickSession(info base.ApiCtrlKickSessionReq) (ret base.ApiCtrlKickSessionResp) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	g := sm.getGroup("", info.StreamName)
	if g == nil {
		ret.ErrorCode = base.ErrorCodeGroupNotFound
		ret.Desp = base.DespGroupNotFound
		return
	}

	if !g.KickSession(info.SessionId) {
		ret.ErrorCode = base.ErrorCodeSessionNotFound
		ret.Desp = base.DespSessionNotFound
		return
	}

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	return
}

func (sm *ServerManager) CtrlAddIpBlacklist(info base.ApiCtrlAddIpBlacklistReq) (ret base.ApiCtrlAddIpBlacklistResp) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.ipBlacklist.Add(info.Ip, info.DurationSec)

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	return
}

func (sm *ServerManager) CtrlStartRtpPub(info base.ApiCtrlStartRtpPubReq) (ret base.ApiCtrlStartRtpPubResp) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// 注意，如果group不存在，我们依然relay pull
	g := sm.getOrCreateGroup("", info.StreamName)
	ret = g.StartRtpPub(info)

	return
}

type httpflvPuller struct {
	session *httpflv.PullSession
	app     string
	stream  string
	cps     ICustomizePubSessionContext
	group   *Group
}

func (sm *ServerManager) CtrlStartHttpflvPull(info base.ApiCtrlStartHttpflvPullReq) base.ApiCtrlStartHttpflvPullResp {

	var ret base.ApiCtrlStartHttpflvPullResp

	app := info.AppName
	stream := info.StreamName
	url := info.Url
	if app == "" || stream == "" || url == "" {
		ret.ErrorCode = base.ErrorCodeHttpflvInvalidParam
		ret.Desp = "missing app_name/stream_name/url"
		return ret
	}

	//group, _ := sm.groupManager.GetOrCreateGroup(app, stream)
	group := sm.getOrCreateGroup(app, stream)

	// Create custom publisher for this group/stream.
	cps, err := group.AddCustomizePubSession(stream)
	if err != nil {
		ret.ErrorCode = base.ErrorCodeHttpflvDupInStream
		ret.Desp = err.Error()
		return ret
	}

	// Build HTTP-FLV pull session.
	ps := httpflv.NewPullSession(func(opt *httpflv.PullSessionOption) {
		if info.PullTimeoutMs > 0 {
			opt.PullTimeoutMs = info.PullTimeoutMs
		}
		if info.ReadTimeoutMs > 0 {
			opt.ReadTimeoutMs = info.ReadTimeoutMs
		}
	}).WithOnReadFlvTag(func(tag httpflv.Tag) {
		// Convert FLV Tag -> RTMP Msg and feed into lal
		msg := remux.FlvTag2RtmpMsg(tag)
		_ = cps.FeedRtmpMsg(msg)
	})

	// Track and run
	sid := fmt.Sprintf("httpflv-pull-%d", time.Now().UnixNano())
	sm.httpflvPullers.Store(sid, &httpflvPuller{
		session: ps,
		app:     app,
		stream:  stream,
		cps:     cps,
		group:   group,
	})

	go func() {
		err := ps.Start(url)
		// Cleanup when Start returns (error or stop)
		if _, ok := sm.httpflvPullers.Load(sid); ok {
			sm.httpflvPullers.Delete(sid)
			//group.DelCustomizePubSession(cps)
		}
		_ = err // optionally log
	}()

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.SessionId = sid
	ret.Data.AppName = app
	ret.Data.StreamName = stream
	return ret
}

func (sm *ServerManager) CtrlStopHttpflvPull(req base.ApiCtrlStopHttpflvPullReq) base.ApiCtrlStopHttpflvPullResp {
	var ret base.ApiCtrlStopHttpflvPullResp

	// Resolve session by id or (app, stream)
	sid := req.SessionId
	if sid == "" {
		sm.httpflvPullers.Range(func(key, value any) bool {
			p := value.(*httpflvPuller)
			if p.app == req.AppName && p.stream == req.StreamName {
				sid = key.(string)
				return false
			}
			return true
		})
		if sid == "" {
			ret.ErrorCode = base.ErrorCodeSessionNotFound
			ret.Desp = base.DespSessionNotFound
			return ret
		}
	}

	if v, ok := sm.httpflvPullers.Load(sid); ok {
		p := v.(*httpflvPuller)
		_ = p.session.Dispose()               // stop upstream pull
		p.group.DelCustomizePubSession(p.cps) // detach publisher
		sm.httpflvPullers.Delete(sid)

		ret.ErrorCode = base.ErrorCodeSucc
		ret.Desp = base.DespSucc
		return ret
	}

	ret.ErrorCode = base.ErrorCodeSessionNotFound
	ret.Desp = base.DespSessionNotFound
	return ret
}

type wsflvPuller struct {
	session *WsFlvPullSession
	app     string
	stream  string
	cps     ICustomizePubSessionContext
	group   *Group
}

func (sm *ServerManager) CtrlStartWsflvPull(info base.ApiCtrlStartWsflvPullReq) base.ApiCtrlStartWsflvPullResp {
	var ret base.ApiCtrlStartWsflvPullResp

	app := info.AppName
	stream := info.StreamName
	url := info.Url
	header := info.Headers

	if app == "" || stream == "" || url == "" {
		ret.ErrorCode = base.ErrorCodeHttpflvInvalidParam
		ret.Desp = "missing app_name/stream_name/url"
		return ret
	}

	// CRITICAL: protect with mutex
	sm.mutex.Lock()

	group := sm.getOrCreateGroup(app, stream)

	cps, err := group.AddCustomizePubSession(stream)

	sm.mutex.Unlock()

	if err != nil {
		ret.ErrorCode = base.ErrorCodeHttpflvDupInStream
		ret.Desp = err.Error()
		return ret
	}

	ps := NewWsFlvPullSession(app, stream, group, cps)

	sid := fmt.Sprintf("wsflv-pull-%d", time.Now().UnixNano())

	sm.wsflvPullers.Store(sid, &wsflvPuller{
		session: ps,
		app:     app,
		stream:  stream,
		cps:     cps,
		group:   group,
	})

	go func() {
		//insert url to pull, and the request headers if exists
		err := ps.Start(url, header)

		// cleanup safely
		sm.mutex.Lock()

		if v, ok := sm.wsflvPullers.Load(sid); ok {

			p := v.(*wsflvPuller)

			p.group.DelCustomizePubSession(p.cps)

			sm.wsflvPullers.Delete(sid)
		}

		sm.mutex.Unlock()

		if err != nil {
			Log.Errorf("wsflv pull stopped. err=%v", err)
		}
	}()

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.SessionId = sid
	ret.Data.AppName = app
	ret.Data.StreamName = stream

	return ret
}

func (sm *ServerManager) CtrlStopWsflvPull(req base.ApiCtrlStopWsflvPullReq) base.ApiCtrlStopWsflvPullResp {

	var ret base.ApiCtrlStopWsflvPullResp

	sid := req.SessionId

	if sid == "" {

		sm.wsflvPullers.Range(func(key, value any) bool {

			p := value.(*wsflvPuller)

			if p.app == req.AppName && p.stream == req.StreamName {
				sid = key.(string)
				return false
			}

			return true
		})

		if sid == "" {
			ret.ErrorCode = base.ErrorCodeSessionNotFound
			ret.Desp = base.DespSessionNotFound
			return ret
		}
	}

	v, ok := sm.wsflvPullers.Load(sid)

	if !ok {
		ret.ErrorCode = base.ErrorCodeSessionNotFound
		ret.Desp = base.DespSessionNotFound
		return ret
	}

	p := v.(*wsflvPuller)

	// Stop session first (no mutex needed)
	p.session.Stop()

	// CRITICAL: mutex required for group operation
	sm.mutex.Lock()

	p.group.DelCustomizePubSession(p.cps)

	sm.wsflvPullers.Delete(sid)

	sm.mutex.Unlock()

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.SessionId = sid

	return ret
}

func (sm *ServerManager) GetWsflvPullStats(app, stream string) (WsFlvPullStats, bool) {

	var result WsFlvPullStats
	var found bool

	sm.wsflvPullers.Range(func(_, value any) bool {

		p := value.(*wsflvPuller)

		if p.app == app && p.stream == stream {

			result = p.session.GetStats()
			found = true
			return false
		}

		return true
	})

	return result, found
}

func (sm *ServerManager) GetAllWsflvPullStats() []WsFlvPullStats {

	var list []WsFlvPullStats

	sm.wsflvPullers.Range(func(_, value any) bool {

		p := value.(*wsflvPuller)

		list = append(list, p.session.GetStats())

		return true
	})

	return list
}
