// Copyright 2022, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package logic

import (
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/remux"
	"github.com/q191201771/naza/pkg/nazaatomic"
	"github.com/q191201771/naza/pkg/nazalog"
)

type CustomizePubSessionOption struct {
	DebugDumpPacket string
}

type ModCustomizePubSessionOptionFn func(option *CustomizePubSessionOption)

type CustomizePubSessionContext struct {
	uniqueKey string

	streamName string
	remuxer    *remux.AvPacket2RtmpRemuxer
	onRtmpMsg  func(msg base.RtmpMsg)
	option     CustomizePubSessionOption
	dumpFile   *base.DumpFile

	disposeFlag nazaatomic.Bool
	ControlSid  string
}

func NewCustomizePubSessionContext(streamName string) *CustomizePubSessionContext {
	s := &CustomizePubSessionContext{
		uniqueKey:  base.GenUkCustomizePubSession(),
		streamName: streamName,
		remuxer:    remux.NewAvPacket2RtmpRemuxer(),
	}
	nazalog.Infof("[%s] NewCustomizePubSessionContext.", s.uniqueKey)
	return s
}

func (ctx *CustomizePubSessionContext) WithOnRtmpMsg(onRtmpMsg func(msg base.RtmpMsg)) *CustomizePubSessionContext {
	ctx.onRtmpMsg = onRtmpMsg
	ctx.remuxer.WithOnRtmpMsg(onRtmpMsg)
	return ctx
}

func (ctx *CustomizePubSessionContext) WithCustomizePubSessionContextOption(modFn func(option *CustomizePubSessionOption)) *CustomizePubSessionContext {
	modFn(&ctx.option)
	if ctx.option.DebugDumpPacket != "" {
		ctx.dumpFile = base.NewDumpFile()
		err := ctx.dumpFile.OpenToWrite(ctx.option.DebugDumpPacket)
		nazalog.Assert(nil, err)
	}
	return ctx
}

func (ctx *CustomizePubSessionContext) UniqueKey() string {
	return ctx.uniqueKey
}

func (ctx *CustomizePubSessionContext) StreamName() string {
	return ctx.streamName
}

func (ctx *CustomizePubSessionContext) Dispose() {
	nazalog.Infof("[%s] CustomizePubSessionContext::Dispose.", ctx.uniqueKey)
	ctx.disposeFlag.Store(true)
}

// -----implement of base.IAvPacketStream ------------------------------------------------------------------------------

func (ctx *CustomizePubSessionContext) WithOption(modOption func(option *base.AvPacketStreamOption)) {
	ctx.remuxer.WithOption(modOption)
}

func (ctx *CustomizePubSessionContext) FeedAudioSpecificConfig(asc []byte) error {
	if ctx.disposeFlag.Load() {
		nazalog.Errorf("[%s] FeedAudioSpecificConfig while CustomizePubSessionContext disposed.", ctx.uniqueKey)
		return base.ErrDisposedInStream
	}
	//nazalog.Debugf("[%s] FeedAudioSpecificConfig. asc=%s", ctx.uniqueKey, hex.Dump(asc))
	if ctx.dumpFile != nil {
		_ = ctx.dumpFile.WriteWithType(asc, base.DumpTypeCustomizePubAudioSpecificConfigData)
	}
	ctx.remuxer.InitWithAvConfig(asc, nil, nil, nil)
	return nil
}

func (ctx *CustomizePubSessionContext) FeedAvPacket(packet base.AvPacket) error {
	if ctx.disposeFlag.Load() {
		nazalog.Errorf("[%s] FeedAudioSpecificConfig while CustomizePubSessionContext disposed.", ctx.uniqueKey)
		return base.ErrDisposedInStream
	}
	//nazalog.Debugf("[%s] FeedAvPacket. packet=%s", ctx.uniqueKey, packet.DebugString())
	if ctx.dumpFile != nil {
		_ = ctx.dumpFile.WriteAvPacket(packet, base.DumpTypeCustomizePubData)
	}
	ctx.remuxer.FeedAvPacket(packet)
	return nil
}

func (ctx *CustomizePubSessionContext) FeedRtmpMsg(msg base.RtmpMsg) error {
	if ctx.disposeFlag.Load() {
		return base.ErrDisposedInStream
	}
	ctx.onRtmpMsg(msg)
	return nil
}

// Add this to logic/customize_pub_session.go

func (ctx *CustomizePubSessionContext) Stat() base.StatPub {
	return base.StatPub{
		StatSession: base.StatSession{
			SessionId:  ctx.UniqueKey(),
			Protocol:   "WS-FLV",
			BaseType:   "PUB",
			RemoteAddr: "upstream", // or track actual remote addr if available
			StartTime:  time.Now().Format("2006-01-02 15:04:05"),
			// Optionally fill in counters if you track them:
			ReadBytesSum:      0,
			WroteBytesSum:     0,
			BitrateKbits:      0,
			ReadBitrateKbits:  0,
			WriteBitrateKbits: 0,
		},
	}
}
