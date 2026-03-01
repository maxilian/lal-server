// Copyright 2019, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package logic

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/q191201771/naza/pkg/taskpool"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/hls"
	"github.com/q191201771/lal/pkg/httpflv"
	"github.com/q191201771/lal/pkg/httpts"
	"github.com/q191201771/lal/pkg/jt1078"
	"github.com/q191201771/lal/pkg/rtmp"
	"github.com/q191201771/lal/pkg/rtsp"
	"github.com/q191201771/naza/pkg/defertaskthread"

	//"github.com/felixge/fgprof"
	"github.com/q191201771/lal/pkg/aac"
)

type ServerManager struct {
	option          Option
	serverStartTime string
	config          *Config

	httpServerManager *base.HttpServerManager
	httpServerHandler *HttpServerHandler
	hlsServerHandler  *hls.ServerHandler

	rtmpServer    *rtmp.Server
	rtmpsServer   *rtmp.Server
	rtspServer    *rtsp.Server
	rtspsServer   *rtsp.Server
	jt1078Server  *jt1078.Server
	httpApiServer *HttpApiServer
	pprofServer   *http.Server
	wsrtspServer  *rtsp.WebsocketServer
	exitChan      chan struct{}

	mutex        sync.Mutex
	groupManager IGroupManager

	onHookSession func(uniqueKey string, streamName string) ICustomizeHookSessionContext

	notifyHandlerThread taskpool.Pool

	ipBlacklist    IpBlacklist
	httpflvPullers sync.Map
	wsflvPullers   sync.Map
	jtSessions     sync.Map
}

func NewServerManager(modOption ...ModOption) *ServerManager {
	sm := &ServerManager{
		serverStartTime: base.ReadableNowTime(),
		exitChan:        make(chan struct{}, 1),
	}
	sm.groupManager = NewSimpleGroupManager(sm)

	sm.option = defaultOption
	for _, fn := range modOption {
		fn(&sm.option)
	}

	rawContent := sm.option.ConfRawContent
	if len(rawContent) == 0 {
		rawContent = base.WrapReadConfigFile(sm.option.ConfFilename, base.LalDefaultConfFilenameList, func() {
			_, _ = fmt.Fprintf(os.Stderr, `
Example:
  %s -c %s

Github: %s
Doc: %s
`, os.Args[0], filepath.FromSlash("./conf/"+base.LalDefaultConfigFilename), base.LalGithubSite, base.LalDocSite)
		})
	}
	sm.config = LoadConfAndInitLog(rawContent)
	base.LogoutStartInfo()

	if sm.config.HlsConfig.Enable && sm.config.HlsConfig.UseMemoryAsDiskFlag {
		Log.Infof("hls use memory as disk.")
		hls.SetUseMemoryAsDiskFlag(true)
	}

	if sm.config.RecordConfig.EnableFlv {
		if err := os.MkdirAll(sm.config.RecordConfig.FlvOutPath, 0777); err != nil {
			Log.Errorf("record flv mkdir error. path=%s, err=%+v", sm.config.RecordConfig.FlvOutPath, err)
		}
	}

	if sm.config.RecordConfig.EnableMpegts {
		if err := os.MkdirAll(sm.config.RecordConfig.MpegtsOutPath, 0777); err != nil {
			Log.Errorf("record mpegts mkdir error. path=%s, err=%+v", sm.config.RecordConfig.MpegtsOutPath, err)
		}
	}

	sm.nhInitNotifyHandler()

	if sm.config.HttpflvConfig.Enable || sm.config.HttpflvConfig.EnableHttps ||
		sm.config.HttptsConfig.Enable || sm.config.HttptsConfig.EnableHttps ||
		sm.config.HlsConfig.Enable || sm.config.HlsConfig.EnableHttps {
		sm.httpServerManager = base.NewHttpServerManager()
		sm.httpServerHandler = NewHttpServerHandler(sm)
		sm.hlsServerHandler = hls.NewServerHandler(sm.config.HlsConfig.OutPath, sm.config.HlsConfig.UrlPattern, sm.config.HlsConfig.SubSessionHashKey, sm.config.HlsConfig.SubSessionTimeoutMs, sm)
	}

	if sm.config.RtmpConfig.Enable {
		sm.rtmpServer = rtmp.NewServer(sm.config.RtmpConfig.Addr, sm)
	}
	if sm.config.RtmpConfig.RtmpsEnable {
		sm.rtmpsServer = rtmp.NewServer(sm.config.RtmpConfig.RtmpsAddr, sm)
	}
	if sm.config.RtspConfig.Enable {
		sm.rtspServer = rtsp.NewServer(sm.config.RtspConfig.Addr, sm, sm.config.RtspConfig.ServerAuthConfig)
	}
	if sm.config.RtspConfig.RtspsEnable {
		sm.rtspsServer = rtsp.NewServer(sm.config.RtspConfig.RtspsAddr, sm, sm.config.RtspConfig.ServerAuthConfig)
	}
	if sm.config.RtspConfig.WsRtspEnable {
		sm.wsrtspServer = rtsp.NewWebsocketServer(sm.config.RtspConfig.WsRtspAddr, sm, sm.config.RtspConfig.ServerAuthConfig)
	}
	if sm.config.HttpApiConfig.Enable {
		sm.httpApiServer = NewHttpApiServer(sm.config.HttpApiConfig.Addr, sm)
	}

	if sm.config.PprofConfig.Enable {
		sm.pprofServer = &http.Server{Addr: sm.config.PprofConfig.Addr, Handler: nil}
	}

	if sm.option.Authentication == nil {
		sm.option.Authentication = NewSimpleAuthCtx(sm.config.SimpleAuthConfig)
	}

	if sm.config.Jt1078.Enable {
		sm.jt1078Server = jt1078.NewServer(sm.config.Jt1078.Addr, sm)
		if err := sm.jt1078Server.Listen(); err != nil {
			Log.Errorf("JT1078 start failed: %v", err)
			return sm
		}
		go sm.jt1078Server.RunLoop()

		Log.Infof("JT1078 server started at %s", sm.config.Jt1078.Addr)
	}

	return sm
}

// ----- implement ILalServer interface --------------------------------------------------------------------------------

func (sm *ServerManager) RunLoop() error {
	// TODO(chef): 作为阻塞函数，外部只能获取失败或结束的信息，没法获取到启动成功的信息

	sm.nhOnServerStart(sm.StatLalInfo())

	if sm.pprofServer != nil {
		go func() {
			//Log.Warn("start fgprof.")
			//http.DefaultServeMux.Handle("/debug/fgprof", fgprof.Handler())
			Log.Infof("start web pprof listen. addr=%s", sm.config.PprofConfig.Addr)
			if err := sm.pprofServer.ListenAndServe(); err != nil {
				Log.Error(err)
			}
		}()
	}

	go base.RunSignalHandler(func() {
		sm.Dispose()
	})

	var addMux = func(config CommonHttpServerConfig, handler base.Handler, name string) error {
		if config.Enable {
			err := sm.httpServerManager.AddListen(
				base.LocalAddrCtx{Addr: config.HttpListenAddr},
				config.UrlPattern,
				handler,
			)
			if err != nil {
				Log.Errorf("add http listen for %s failed. addr=%s, pattern=%s, err=%+v", name, config.HttpListenAddr, config.UrlPattern, err)
				return err
			}
			Log.Infof("add http listen for %s. addr=%s, pattern=%s", name, config.HttpListenAddr, config.UrlPattern)
		}
		if config.EnableHttps {
			err := sm.httpServerManager.AddListen(
				base.LocalAddrCtx{IsHttps: true, Addr: config.HttpsListenAddr, CertFile: config.HttpsCertFile, KeyFile: config.HttpsKeyFile},
				config.UrlPattern,
				handler,
			)
			if err != nil {
				Log.Errorf("add https listen for %s failed. addr=%s, pattern=%s, err=%+v", name, config.HttpsListenAddr, config.UrlPattern, err)
			} else {
				Log.Infof("add https listen for %s. addr=%s, pattern=%s", name, config.HttpsListenAddr, config.UrlPattern)
			}
		}
		return nil
	}

	if err := addMux(sm.config.HttpflvConfig.CommonHttpServerConfig, sm.httpServerHandler.ServeSubSession, "httpflv"); err != nil {
		return err
	}
	if err := addMux(sm.config.HttptsConfig.CommonHttpServerConfig, sm.httpServerHandler.ServeSubSession, "httpts"); err != nil {
		return err
	}
	if err := addMux(sm.config.HlsConfig.CommonHttpServerConfig, sm.serveHls, "hls"); err != nil {
		return err
	}

	if sm.httpServerManager != nil {
		go func() {
			if err := sm.httpServerManager.RunLoop(); err != nil {
				Log.Error(err)
			}
		}()
	}

	if sm.rtmpServer != nil {
		if err := sm.rtmpServer.Listen(); err != nil {
			return err
		}
		go func() {
			if err := sm.rtmpServer.RunLoop(); err != nil {
				Log.Error(err)
			}
		}()
	}

	if sm.rtmpsServer != nil {
		err := sm.rtmpsServer.ListenWithTLS(sm.config.RtmpConfig.RtmpsCertFile, sm.config.RtmpConfig.RtmpsKeyFile)
		// rtmps启动失败影响降级：当rtmps启动时我们并不返回错误，保证不因为rtmps影响其他服务
		if err == nil {
			go func() {
				if errRun := sm.rtmpsServer.RunLoop(); errRun != nil {
					Log.Error(errRun)
				}
			}()
		}
	}

	if sm.rtspServer != nil {
		if err := sm.rtspServer.Listen(); err != nil {
			return err
		}
		go func() {
			if err := sm.rtspServer.RunLoop(); err != nil {
				Log.Error(err)
			}
		}()
	}

	if sm.rtspsServer != nil {
		err := sm.rtspsServer.ListenWithTLS(sm.config.RtspConfig.RtspsCertFile, sm.config.RtspConfig.RtspsKeyFile)
		// rtsps启动失败影响降级：当rtsps启动时我们并不返回错误，保证不因为rtsps影响其他服务
		if err == nil {
			go func() {
				if errRun := sm.rtspsServer.RunLoop(); errRun != nil {
					Log.Error(errRun)
				}
			}()
		}
	}

	if sm.wsrtspServer != nil {
		go func() {
			err := sm.wsrtspServer.Listen()
			if err != nil {
				Log.Error(err)
			}
		}()
	}

	if sm.httpApiServer != nil {
		if err := sm.httpApiServer.Listen(); err != nil {
			return err
		}
		go func() {
			if err := sm.httpApiServer.RunLoop(); err != nil {
				Log.Error(err)
			}
		}()
	}

	uis := uint32(sm.config.HttpNotifyConfig.UpdateIntervalSec)
	var updateInfo base.UpdateInfo
	updateInfo.Groups = sm.StatAllGroup()
	sm.nhOnUpdate(updateInfo)

	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	var tickCount uint32
	for {
		select {
		case <-sm.exitChan:
			return nil
		case <-t.C:
			tickCount++

			sm.mutex.Lock()

			// 关闭空闲的group
			sm.groupManager.Iterate(func(group *Group) bool {
				if group.IsInactive() {
					Log.Infof("erase inactive group. [%s]", group.UniqueKey)
					group.Dispose()
					return false
				}

				group.Tick(tickCount)
				return true
			})

			// 定时打印一些group相关的debug日志
			if sm.config.DebugConfig.LogGroupIntervalSec > 0 &&
				tickCount%uint32(sm.config.DebugConfig.LogGroupIntervalSec) == 0 {
				groupNum := sm.groupManager.Len()
				Log.Debugf("DEBUG_GROUP_LOG: group size=%d", groupNum)
				if sm.config.DebugConfig.LogGroupMaxGroupNum > 0 {
					var loggedGroupCount int
					sm.groupManager.Iterate(func(group *Group) bool {
						loggedGroupCount++
						if loggedGroupCount <= sm.config.DebugConfig.LogGroupMaxGroupNum {
							Log.Debugf("DEBUG_GROUP_LOG: %d %s", loggedGroupCount, group.StringifyDebugStats(sm.config.DebugConfig.LogGroupMaxSubNumPerGroup))
						}
						return true
					})
				}
			}

			sm.mutex.Unlock()

			// 定时通过http notify发送group相关的信息
			if uis != 0 && (tickCount%uis) == 0 {
				updateInfo.Groups = sm.StatAllGroup()
				sm.nhOnUpdate(updateInfo)
			}
		}
	}

	// never reach here
}

func (sm *ServerManager) Dispose() {
	Log.Debug("dispose server manager.")

	if sm.rtmpServer != nil {
		sm.rtmpServer.Dispose()
	}

	if sm.rtmpsServer != nil {
		sm.rtmpsServer.Dispose()
	}

	if sm.rtspServer != nil {
		sm.rtspServer.Dispose()
	}

	if sm.rtspsServer != nil {
		sm.rtspsServer.Dispose()
	}

	if sm.httpServerManager != nil {
		sm.httpServerManager.Dispose()
	}

	if sm.pprofServer != nil {
		sm.pprofServer.Close()
	}

	//if sm.hlsServer != nil {
	//	sm.hlsServer.Dispose()
	//}

	sm.mutex.Lock()
	sm.groupManager.Iterate(func(group *Group) bool {
		group.Dispose()
		return true
	})
	sm.mutex.Unlock()

	sm.exitChan <- struct{}{}
}

// ---------------------------------------------------------------------------------------------------------------------

func (sm *ServerManager) AddCustomizePubSession(streamName string) (ICustomizePubSessionContext, error) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	group := sm.getOrCreateGroup("", streamName)
	return group.AddCustomizePubSession(streamName)
}

func (sm *ServerManager) DelCustomizePubSession(sessionCtx ICustomizePubSessionContext) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	group := sm.getGroup("", sessionCtx.StreamName())
	if group == nil {
		return
	}
	group.DelCustomizePubSession(sessionCtx)
}

func (sm *ServerManager) WithOnHookSession(onHookSession func(uniqueKey string, streamName string) ICustomizeHookSessionContext) {
	sm.onHookSession = onHookSession
}

// ----- implement rtmp.IServerObserver interface -----------------------------------------------------------------------

func (sm *ServerManager) OnRtmpConnect(session *rtmp.ServerSession, opa rtmp.ObjectPairArray) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	var info base.RtmpConnectInfo
	info.SessionId = session.UniqueKey()
	info.RemoteAddr = session.GetStat().RemoteAddr
	info.App, _ = opa.FindString("app")
	info.FlashVer, _ = opa.FindString("flashVer")
	info.TcUrl, _ = opa.FindString("tcUrl")
	sm.nhOnRtmpConnect(info)
}

func (sm *ServerManager) OnNewRtmpPubSession(session *rtmp.ServerSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2PubStartInfo(session)

	// 先做simple auth鉴权
	if err := sm.option.Authentication.OnPubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	if err := group.AddRtmpPubSession(session); err != nil {
		return err
	}

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnPubStart(info)
	return nil
}

func (sm *ServerManager) OnDelRtmpPubSession(session *rtmp.ServerSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelRtmpPubSession(session)

	info := base.Session2PubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnPubStop(info)
}

func (sm *ServerManager) OnNewRtmpSubSession(session *rtmp.ServerSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2SubStartInfo(session)

	if err := sm.option.Authentication.OnSubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	group.AddRtmpSubSession(session)

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnSubStart(info)
	return nil
}

func (sm *ServerManager) OnDelRtmpSubSession(session *rtmp.ServerSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelRtmpSubSession(session)

	info := base.Session2SubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnSubStop(info)
}

// ----- implement IHttpServerHandlerObserver interface -----------------------------------------------------------------

func (sm *ServerManager) OnNewHttpflvSubSession(session *httpflv.SubSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2SubStartInfo(session)

	if err := sm.option.Authentication.OnSubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	group.AddHttpflvSubSession(session)

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnSubStart(info)
	return nil
}

func (sm *ServerManager) OnDelHttpflvSubSession(session *httpflv.SubSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelHttpflvSubSession(session)

	info := base.Session2SubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnSubStop(info)
}

func (sm *ServerManager) OnNewHttptsSubSession(session *httpts.SubSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2SubStartInfo(session)

	if err := sm.option.Authentication.OnSubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	group.AddHttptsSubSession(session)

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnSubStart(info)

	return nil
}

func (sm *ServerManager) OnDelHttptsSubSession(session *httpts.SubSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelHttptsSubSession(session)

	info := base.Session2SubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnSubStop(info)
}

// ----- implement rtsp.IServerObserver interface -----------------------------------------------------------------------

func (sm *ServerManager) OnNewRtspSessionConnect(session *rtsp.ServerCommandSession) {
	// TODO chef: impl me
}

func (sm *ServerManager) OnDelRtspSession(session *rtsp.ServerCommandSession) {
	// TODO chef: impl me
}

func (sm *ServerManager) OnNewRtspPubSession(session *rtsp.PubSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2PubStartInfo(session)

	if err := sm.option.Authentication.OnPubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	if err := group.AddRtspPubSession(session); err != nil {
		return err
	}

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnPubStart(info)
	return nil
}

func (sm *ServerManager) OnDelRtspPubSession(session *rtsp.PubSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelRtspPubSession(session)

	info := base.Session2PubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnPubStop(info)
}

func (sm *ServerManager) OnNewRtspSubSessionDescribe(session *rtsp.SubSession) (ok bool, sdp []byte) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2SubStartInfo(session)

	if err := sm.option.Authentication.OnSubStart(info); err != nil {
		return false, nil
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	ok, sdp = group.HandleNewRtspSubSessionDescribe(session)
	if !ok {
		return
	}

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnSubStart(info)
	return
}

func (sm *ServerManager) OnNewRtspSubSessionPlay(session *rtsp.SubSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	group.HandleNewRtspSubSessionPlay(session)
	return nil
}

func (sm *ServerManager) OnDelRtspSubSession(session *rtsp.SubSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelRtspSubSession(session)

	info := base.Session2SubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnSubStop(info)
}

func (sm *ServerManager) OnNewHlsSubSession(session *hls.SubSession) error {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	info := base.Session2SubStartInfo(session)

	if err := sm.option.Authentication.OnSubStart(info); err != nil {
		return err
	}

	group := sm.getOrCreateGroup(session.AppName(), session.StreamName())
	group.AddHlsSubSession(session)

	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()

	sm.nhOnSubStart(info)

	return nil
}

func (sm *ServerManager) OnDelHlsSubSession(session *hls.SubSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	group := sm.getGroup(session.AppName(), session.StreamName())
	if group == nil {
		return
	}

	group.DelHlsSubSession(session)

	info := base.Session2SubStopInfo(session)
	info.HasInSession = group.HasInSession()
	info.HasOutSession = group.HasOutSession()
	sm.nhOnSubStop(info)
}

// ----- implement IGroupCreator interface -----------------------------------------------------------------------------

func (sm *ServerManager) CreateGroup(appName string, streamName string) *Group {
	var config *Config
	if sm.option.ModConfigGroupCreator != nil {
		cloneConfig := *sm.config
		sm.option.ModConfigGroupCreator(appName, streamName, &cloneConfig)
		config = &cloneConfig
	} else {
		config = sm.config
	}
	option := GroupOption{
		onHookSession: sm.onHookSession,
	}
	return NewGroup(appName, streamName, config, option, sm)
}

// ----- implement IGroupObserver interface -----------------------------------------------------------------------------

func (sm *ServerManager) CleanupHlsIfNeeded(appName string, streamName string, path string) {
	if sm.config.HlsConfig.Enable &&
		(sm.config.HlsConfig.CleanupMode == hls.CleanupModeInTheEnd || sm.config.HlsConfig.CleanupMode == hls.CleanupModeAsap) {
		defertaskthread.Go(
			sm.config.HlsConfig.FragmentDurationMs*(sm.config.HlsConfig.FragmentNum+sm.config.HlsConfig.DeleteThreshold),
			func(param ...interface{}) {
				an := param[0].(string)
				sn := param[1].(string)
				outPath := param[2].(string)

				if g := sm.GetGroup(an, sn); g != nil {
					if g.IsHlsMuxerAlive() {
						Log.Warnf("cancel cleanup hls file path since hls muxer still alive. streamName=%s", sn)
						return
					}
				}

				Log.Infof("cleanup hls file path. streamName=%s, path=%s", sn, outPath)
				if err := hls.RemoveAll(outPath); err != nil {
					Log.Warnf("cleanup hls file path error. path=%s, err=%+v", outPath, err)
				}
			},
			appName,
			streamName,
			path,
		)
	}
}

func (sm *ServerManager) OnRelayPullStart(info base.PullStartInfo) {
	sm.nhOnRelayPullStart(info)
}

func (sm *ServerManager) OnRelayPullStop(info base.PullStopInfo) {
	sm.nhOnRelayPullStop(info)
}

func (sm *ServerManager) OnHlsMakeTs(info base.HlsMakeTsInfo) {
	sm.nhOnHlsMakeTs(info)
}

// ---------------------------------------------------------------------------------------------------------------------

func (sm *ServerManager) Config() *Config {
	return sm.config
}

func (sm *ServerManager) GetGroup(appName string, streamName string) *Group {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	return sm.getGroup(appName, streamName)
}

// ----- private method ------------------------------------------------------------------------------------------------

// 注意，函数内部不加锁，由调用方保证加锁进入
func (sm *ServerManager) getOrCreateGroup(appName string, streamName string) *Group {
	g, createFlag := sm.groupManager.GetOrCreateGroup(appName, streamName)
	if createFlag {
		go g.RunLoop()
	}
	return g
}

func (sm *ServerManager) getGroup(appName string, streamName string) *Group {
	return sm.groupManager.GetGroup(appName, streamName)
}

func (sm *ServerManager) serveHls(writer http.ResponseWriter, req *http.Request) {
	urlCtx, err := base.ParseUrl(base.ParseHttpRequest(req), 80)
	if err != nil {
		Log.Errorf("parse url. err=%+v", err)
		return
	}

	if urlCtx.GetFileType() == "m3u8" {
		// TODO(chef): [refactor] 需要整理，这里使用 hls.PathStrategy 不太好 202207
		streamName := hls.PathStrategy.GetRequestInfo(urlCtx, sm.config.HlsConfig.OutPath).StreamName
		if err = sm.option.Authentication.OnHls(streamName, urlCtx.RawQuery); err != nil {
			Log.Errorf("simple auth failed. err=%+v", err)
			return
		}
	}

	remoteIp, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		Log.Warnf("SplitHostPort failed. addr=%s, err=%+v", req.RemoteAddr, err)
		return
	}

	if sm.ipBlacklist.Has(remoteIp) {
		//Log.Warnf("found %s in ip blacklist, so do not serve this request.", remoteIp)

		sm.hlsServerHandler.CloseSubSessionIfExist(req)

		writer.WriteHeader(http.StatusNotFound)
		return
	}

	sm.hlsServerHandler.ServeHTTP(writer, req)
}

type jtPubWrapper struct {
	pub          ICustomizePubSessionContext
	fuBuffer     []byte
	aacSeqHeader []byte
}

func (sm *ServerManager) OnNewJt1078PubSession(
	session *jt1078.ServerSession,
) error {

	streamName := fmt.Sprintf("jt1078_%s", session.UniqueKey())

	// 1️⃣ Create group with custom app
	group := sm.getOrCreateGroup("streaming", streamName)

	// 2️⃣ Create customize pub session inside that group
	pub, err := group.AddCustomizePubSession(streamName)
	if err != nil {
		Log.Errorf("AddCustomizePubSession failed: %v", err)
		return err
	}

	sm.jtSessions.Store(session.UniqueKey(), &jtPubWrapper{
		pub: pub,
	})

	Log.Infof("JT1078 stream created: %s", streamName)

	return nil
}
func (sm *ServerManager) OnJt1078Frame(
	session *jt1078.ServerSession,
	payload []byte,
	timestamp uint32,
) {
	v, ok := sm.jtSessions.Load(session.UniqueKey())
	if !ok {
		return
	}
	wrapper := v.(*jtPubWrapper)

	if len(payload) <= 12 {
		return
	}

	// Remove JT1078 RTP-like header
	payload = payload[12:]

	// Detect AAC by ADTS syncword
	if len(payload) > 1 && payload[0] == 0xFF && (payload[1]&0xF0) == 0xF0 {
		sm.handleAAC(wrapper, payload, timestamp)
		return
	}

	// Otherwise treat as H.264 video
	nalType := payload[0] & 0x1F
	if nalType == 28 {
		sm.handleFuA(wrapper, payload, timestamp)
		return
	}

	nal := payload
	size := uint32(len(nal))

	buf := make([]byte, 4+len(nal))
	binary.BigEndian.PutUint32(buf[:4], size)
	copy(buf[4:], nal)

	pkt := base.AvPacket{
		PayloadType: base.AvPacketPtAvc,
		Timestamp:   int64(timestamp) / 90,
		Payload:     buf,
	}
	wrapper.pub.FeedAvPacket(pkt)
}

func (sm *ServerManager) handleFuA(
	wrapper *jtPubWrapper,
	payload []byte,
	timestamp uint32,
) {
	if len(payload) < 2 {
		return
	}

	fuIndicator := payload[0]
	fuHeader := payload[1]

	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	nalType := fuHeader & 0x1F

	if start {
		// Reconstruct original NAL header
		nalHeader := (fuIndicator & 0xE0) | nalType

		wrapper.fuBuffer = []byte{nalHeader}
		wrapper.fuBuffer = append(wrapper.fuBuffer, payload[2:]...)
		return
	}

	// Middle or End
	wrapper.fuBuffer = append(wrapper.fuBuffer, payload[2:]...)

	if end {
		// Complete NAL assembled
		nal := wrapper.fuBuffer
		size := uint32(len(nal))

		buf := make([]byte, 4+len(nal))
		binary.BigEndian.PutUint32(buf[:4], size)
		copy(buf[4:], nal)

		pkt := base.AvPacket{
			PayloadType: base.AvPacketPtAvc,
			Timestamp:   int64(timestamp) / 90,
			Payload:     buf,
		}
		wrapper.pub.FeedAvPacket(pkt)

		wrapper.fuBuffer = nil
	}
}

func (sm *ServerManager) OnDelJt1078PubSession(
	session *jt1078.ServerSession,
) {

	v, ok := sm.jtSessions.Load(session.UniqueKey())
	if !ok {
		return
	}

	wrapper := v.(*jtPubWrapper)

	sm.DelCustomizePubSession(wrapper.pub)

	sm.jtSessions.Delete(session.UniqueKey())

	Log.Infof("JT1078 stream removed: %s", session.UniqueKey())
}

func (sm *ServerManager) handleAAC(
	wrapper *jtPubWrapper,
	payload []byte,
	timestamp uint32,
) {
	if len(payload) < aac.AdtsHeaderLength {
		return
	}

	// Send AAC sequence header once
	if wrapper.aacSeqHeader == nil {
		seqHeader, err := aac.MakeAudioDataSeqHeaderWithAdtsHeader(payload[:aac.AdtsHeaderLength])
		if err == nil {
			pkt := base.AvPacket{
				PayloadType: base.AvPacketPtAac,
				Timestamp:   int64(timestamp) / 90,
				Payload:     seqHeader,
			}
			wrapper.pub.FeedAvPacket(pkt)
			wrapper.aacSeqHeader = seqHeader
		}
	}

	// Strip ADTS header, feed raw AAC frame
	rawFrame := payload[aac.AdtsHeaderLength:]
	pkt := base.AvPacket{
		PayloadType: base.AvPacketPtAac,
		Timestamp:   int64(timestamp) / 90,
		Payload:     rawFrame,
	}
	wrapper.pub.FeedAvPacket(pkt)
}
