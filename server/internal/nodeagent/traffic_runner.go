package nodeagent

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// TrafficSender 由 Client 实现；测试注入替身。
type TrafficSender interface {
	ReportTraffic(context.Context, TrafficPending) (nodewire.TrafficReceipt, error)
}

// TrafficRunner 把「采样 → 求增量 → 落盘待确认 → 发送 → 确认后前移基线」串起来。
//
// 次序是这个类型存在的全部意义：
//   - 有待确认上报时**只重发它**，绝不先采新样本。否则那一段流量会被新样本顶掉；
//   - 落盘成功后才发送，发送成功后才前移基线。任何一步中断，重启后都退回到
//     「重发同一个 report_id」，服务端按幂等键去重，既不丢也不重。
type TrafficRunner struct {
	Store   *TrafficStore
	Sampler *WGTrafficSampler
	Sender  TrafficSender
	Clock   clock.Clock
}

type TrafficResult struct {
	State     string // sent | resent | idle
	ItemCount int
	Duplicate bool
}

func (r *TrafficRunner) Step(ctx context.Context) (TrafficResult, error) {
	var out TrafficResult
	if r.Store == nil || r.Sampler == nil || r.Sender == nil || r.Clock == nil {
		return out, errors.New("流量上报未正确配置")
	}
	if pending := r.Store.Pending(); pending != nil {
		receipt, err := r.Sender.ReportTraffic(ctx, *pending)
		if err != nil {
			return out, err
		}
		if err := r.Store.Commit(); err != nil {
			return out, err
		}
		return TrafficResult{State: "resent", ItemCount: len(pending.Items), Duplicate: receipt.Duplicate}, nil
	}

	current, credentials, err := r.Sampler.Sample(ctx)
	if err != nil {
		return out, err
	}
	items, err := Delta(r.Store.Baseline(), current, credentials)
	if err != nil {
		return out, err
	}
	if len(items) == 0 {
		// 没有任何流量：不必打扰控制面，基线也无须前移。
		return TrafficResult{State: "idle"}, nil
	}
	now := r.Clock.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Minute)
	if last := r.Store.LastWindowEnd(); last != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, last); err == nil && parsed.Before(now) {
			start = parsed
		}
	}
	// 服务端拒绝超过 10 分钟的窗口；长时间中断后只声明最近这一段。
	if now.Sub(start) > nodewire.TrafficWindow {
		start = now.Add(-nodewire.TrafficWindow)
	}
	if !start.Before(now) {
		start = now.Add(-time.Second)
	}
	pending := TrafficPending{
		ReportID:    uuid.NewString(),
		WindowStart: start.UTC().Format("2006-01-02T15:04:05Z"),
		WindowEnd:   now.UTC().Format("2006-01-02T15:04:05Z"),
		Items:       items,
		Snapshot:    current,
	}
	if err := r.Store.Stage(pending); err != nil {
		return out, err
	}
	receipt, err := r.Sender.ReportTraffic(ctx, pending)
	if err != nil {
		// 保留 pending，下一轮原样重发。
		return out, err
	}
	if err := r.Store.Commit(); err != nil {
		return out, err
	}
	return TrafficResult{State: "sent", ItemCount: len(items), Duplicate: receipt.Duplicate}, nil
}
