// node-agent sends telemetry only; VPN kernel adapters are not installed.
package main

import (
	"context"
	"errors"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/nodeagent"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/yuhai-vpn/server/internal/safelog"
)

func run(ctx context.Context, args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "init-state" && args[0] != "once") {
		return errors.New("只支持 init-state、once 或无参数常驻运行")
	}
	cfg, err := config.LoadNodeAgent()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Info("节点上报未启用")
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("真实节点采集仅支持Linux")
	}
	clk := clock.Real{}
	client, err := nodeagent.NewClient(cfg.ControlURL, cfg.ClientCert, cfg.ClientKey, cfg.ServerCA, clk)
	if err != nil {
		return err
	}
	defer client.Close()
	if len(args) == 1 && args[0] == "init-state" {
		return nodeagent.InitStore(cfg.StateDir, client.NodeID(), client.URL())
	}
	store, err := nodeagent.OpenStore(cfg.StateDir, client.NodeID(), client.URL())
	if err != nil {
		return err
	}
	defer store.Close()
	runner := &nodeagent.Runner{Store: store, Sender: client, Sampler: &nodeagent.LinuxSampler{Clock: clk, NodeID: client.NodeID(), Interface: cfg.Interface, RuntimeFile: cfg.RuntimeFile}, Clock: clk}
	for {
		if err = ctx.Err(); err != nil {
			return nil
		}
		result, err := runner.Step(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		slog.Info("node heartbeat", "state", result.State, "sequence", result.Sequence)
		if len(args) == 1 && args[0] == "once" {
			if result.State != "accepted" {
				return errors.New("本次上报未获确认，状态已保留")
			}
			return nil
		}
		timer := time.NewTimer(30 * time.Second)
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("node-agent stopped", "err", safelog.Error(err))
		os.Exit(1)
	}
}
