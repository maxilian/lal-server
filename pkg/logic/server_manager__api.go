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
	var ret base.StatGroup
	ret = g.GetStat(math.MaxInt32)
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

	group, _ := sm.groupManager.GetOrCreateGroup(app, stream)

	cps, err := group.AddCustomizePubSession(stream)
	if err != nil {
		ret.ErrorCode = base.ErrorCodeHttpflvDupInStream
		ret.Desp = err.Error()
		return ret
	}

	ps := httpflv.NewPullSession(func(opt *httpflv.PullSessionOption) {
		if info.PullTimeoutMs > 0 {
			opt.PullTimeoutMs = info.PullTimeoutMs
		}
		if info.ReadTimeoutMs > 0 {
			opt.ReadTimeoutMs = info.ReadTimeoutMs
		}
	}).WithOnReadFlvTag(func(tag httpflv.Tag) {
		msg := remux.FlvTag2RtmpMsg(tag)
		_ = cps.FeedRtmpMsg(msg)
	})

	sid := fmt.Sprintf("httpflv-pull-%d", time.Now().UnixNano())

	// Optional metadata into CPS (without changing interface)
	if meta, ok := any(cps).(interface {
		SetControlSessionID(string)
		SetProtocol(string)
		SetRemoteAddr(string)
	}); ok {
		meta.SetControlSessionID(sid)
		meta.SetProtocol("HTTP-FLV")
		meta.SetRemoteAddr(url)
	}

	sm.httpflvPullers.Store(sid, &httpflvPuller{
		session: ps,
		app:     app,
		stream:  stream,
		cps:     cps,
		group:   group,
	})

	go func() {
		err := ps.Start(url)
		sm.httpflvPullers.Delete(sid)
		group.DelCustomizePubSession(cps)
		// sm.tryDeleteGroupIfEmpty(app, stream, group)
		_ = err
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

	sid, p, ok := sm.resolveHttpflvPuller(req.SessionId, req.AppName, req.StreamName)
	if !ok {
		ret.ErrorCode = base.ErrorCodeSessionNotFound
		ret.Desp = base.DespSessionNotFound
		return ret
	}

	_ = p.session.Dispose() // httpflv stop
	p.group.DelCustomizePubSession(p.cps)
	sm.httpflvPullers.Delete(sid)
	// sm.tryDeleteGroupIfEmpty(p.app, p.stream, p.group)

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.SessionId = sid
	ret.Data.AppName = p.app
	ret.Data.StreamName = p.stream
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
	if app == "" || stream == "" || url == "" {
		ret.ErrorCode = base.ErrorCodeHttpflvInvalidParam
		ret.Desp = "missing app_name/stream_name/url"
		return ret
	}

	group, _ := sm.groupManager.GetOrCreateGroup(app, stream)

	// OPTIONAL: auto-takeover any existing wsflv puller for the same (app, stream)
	// var oldSid string
	// var oldPuller *wsflvPuller
	// sm.wsflvPullers.Range(func(key, value any) bool {
	//     p := value.(*wsflvPuller)
	//     if p.app == app && p.stream == stream {
	//         oldSid = key.(string); oldPuller = p; return false
	//     }
	//     return true
	// })
	// if oldPuller != nil {
	//     _ = oldPuller.session.Stop()
	//     oldPuller.group.DelCustomizePubSession(oldPuller.cps)
	//     sm.wsflvPullers.Delete(oldSid)
	//     // sm.tryDeleteGroupIfEmpty(app, stream, oldPuller.group)
	// }

	cps, err := group.AddCustomizePubSession(stream)
	if err != nil {
		ret.ErrorCode = base.ErrorCodeHttpflvDupInStream
		ret.Desp = err.Error()
		return ret
	}

	ps := NewWsFlvPullSession(app, stream, group, cps)

	sid := fmt.Sprintf("wsflv-pull-%d", time.Now().UnixNano())

	// Attach external id and metadata if supported (without changing the interface)
	if meta, ok := any(cps).(interface {
		SetControlSessionID(string)
		SetProtocol(string)
		SetRemoteAddr(string)
	}); ok {
		meta.SetControlSessionID(sid)
		meta.SetProtocol("WS-FLV")
		meta.SetRemoteAddr(url)
	}

	sm.wsflvPullers.Store(sid, &wsflvPuller{
		session: ps,
		app:     app,
		stream:  stream,
		cps:     cps,
		group:   group,
	})

	// Unconditional cleanup when upstream ends (error or normal)
	go func() {
		err := ps.Start(url)
		sm.wsflvPullers.Delete(sid)
		group.DelCustomizePubSession(cps)
		// sm.tryDeleteGroupIfEmpty(app, stream, group)
		_ = err
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

	sid, p, ok := sm.resolveWsflvPuller(req.SessionId, req.AppName, req.StreamName)
	if !ok {
		ret.ErrorCode = base.ErrorCodeSessionNotFound
		ret.Desp = base.DespSessionNotFound
		return ret
	}

	_ = p.session.Stop()
	p.group.DelCustomizePubSession(p.cps)
	sm.wsflvPullers.Delete(sid)
	// sm.tryDeleteGroupIfEmpty(p.app, p.stream, p.group)

	ret.ErrorCode = base.ErrorCodeSucc
	ret.Desp = base.DespSucc
	ret.Data.SessionId = sid
	ret.Data.AppName = p.app
	ret.Data.StreamName = p.stream
	return ret
}

// --- helpers ---------------------------------------------------------------

// resolveWsflvPuller finds a wsflv puller by:
// 1) external sid (req.SessionId),
// 2) internal cps.UniqueKey (if SessionId was internal),
// 3) (app,stream).
func (sm *ServerManager) resolveWsflvPuller(sessionId, app, stream string) (sid string, p *wsflvPuller, ok bool) {
	// 1) external sid
	if sessionId != "" {
		if v, ok := sm.wsflvPullers.Load(sessionId); ok {
			return sessionId, v.(*wsflvPuller), true
		}
		// 2) maybe internal id
		var foundSid string
		var found *wsflvPuller
		sm.wsflvPullers.Range(func(key, value any) bool {
			puller := value.(*wsflvPuller)
			if puller.cps != nil && puller.cps.UniqueKey() == sessionId {
				foundSid = key.(string)
				found = puller
				return false
			}
			return true
		})
		if foundSid != "" {
			return foundSid, found, true
		}
	}
	// 3) (app,stream)
	var foundSid string
	var found *wsflvPuller
	sm.wsflvPullers.Range(func(key, value any) bool {
		puller := value.(*wsflvPuller)
		if (app == "" || puller.app == app) && (stream == "" || puller.stream == stream) {
			foundSid = key.(string)
			found = puller
			return false
		}
		return true
	})
	if foundSid != "" {
		return foundSid, found, true
	}
	return "", nil, false
}

// resolveHttpflvPuller is the same idea for HTTP-FLV.
func (sm *ServerManager) resolveHttpflvPuller(sessionId, app, stream string) (sid string, p *httpflvPuller, ok bool) {
	if sessionId != "" {
		if v, ok := sm.httpflvPullers.Load(sessionId); ok {
			return sessionId, v.(*httpflvPuller), true
		}
		var foundSid string
		var found *httpflvPuller
		sm.httpflvPullers.Range(func(key, value any) bool {
			puller := value.(*httpflvPuller)
			if puller.cps != nil && puller.cps.UniqueKey() == sessionId {
				foundSid = key.(string)
				found = puller
				return false
			}
			return true
		})
		if foundSid != "" {
			return foundSid, found, true
		}
	}
	var foundSid string
	var found *httpflvPuller
	sm.httpflvPullers.Range(func(key, value any) bool {
		puller := value.(*httpflvPuller)
		if (app == "" || puller.app == app) && (stream == "" || puller.stream == stream) {
			foundSid = key.(string)
			found = puller
			return false
		}
		return true
	})
	if foundSid != "" {
		return foundSid, found, true
	}
	return "", nil, false
}
