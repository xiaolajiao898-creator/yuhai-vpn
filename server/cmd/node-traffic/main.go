// node-traffic 按凭据采集 WireGuard 流量并上报控制面。默认关闭。
//
// 它只读取 node-sync 落盘的租约文件来把公钥映射回凭据，自身不下发任何配置、
// 不修改内核状态。基线只在上报被控制面确认后前移，中断后原样重发同一个 report_id。
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/nodeagent"

	"github.com/yuhai-vpn/server/internal/safelog"
)

func run(ctx context.Context, args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "once" && args[0] != "init-state") {
		return errors.New("只支持 init-state、once 或无参数常驻运行")
	}
	cfg, err := config.LoadNodeTraffic()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Info("流量上报未启用")
		return nil
	}
	clk := clock.Real{}
	client, err := nodeagent.NewClient(cfg.ControlURL, cfg.ClientCert, cfg.ClientKey, cfg.ServerCA, clk)
	if err != nil {
		return err
	}
	defer client.Close()
	if len(args) == 1 && args[0] == "init-state" {
		return nodeagent.InitTrafficStore(cfg.StateDir, client.NodeID(), cfg.ControlURL)
	}
	store, err := nodeagent.OpenTrafficStore(cfg.StateDir, client.NodeID(), cfg.ControlURL)
	if err != nil {
		return err
	}
	defer store.Close()
	runner := &nodeagent.TrafficRunner{
		Store: store,
		Sampler: &nodeagent.WGTrafficSampler{
			Clock: clk, NodeID: client.NodeID(), ManifestFile: cfg.ManifestFile,
			Reader: &nodeagent.WGCommand{Binary: cfg.WGBinary}, Interface: cfg.Interface,
		},
		Sender: client,
		Clock:  clk,
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		result, err := runner.Step(ctx)
		if err != nil {
			if len(args) == 1 {
				return err
			}
			// 常驻模式下不因一次失败退出：pending 已落盘，下一轮原样重发。
			slog.Warn("流量上报未送达，将重发同一份", "err", safelog.Error(err))
		} else if result.State != "idle" {
			slog.Info("流量已上报", "state", result.State, "items", result.ItemCount, "duplicate", result.Duplicate)
		}
		if len(args) == 1 {
			return nil
		}
		timer := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("node-traffic stopped", "err", safelog.Error(err))
		os.Exit(1)
	}
}
