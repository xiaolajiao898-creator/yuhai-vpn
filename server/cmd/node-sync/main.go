// node-sync fetches public, short-lived desired state; it never applies peers.
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
	"syscall"
	"time"

	"github.com/yuhai-vpn/server/internal/safelog"
)

func run(ctx context.Context, args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "once" && args[0] != "init-state") {
		return errors.New("只支持 init-state、once 或无参数常驻运行")
	}
	cfg, e := config.LoadNodeSync()
	if e != nil {
		return e
	}
	if !cfg.Enabled {
		slog.Info("WireGuard清单同步未启用")
		return nil
	}
	clk := clock.Real{}
	client, e := nodeagent.NewClient(cfg.ControlURL, cfg.ClientCert, cfg.ClientKey, cfg.ServerCA, clk)
	if e != nil {
		return e
	}
	defer client.Close()
	if len(args) == 1 && args[0] == "init-state" {
		return nodeagent.InitWGSyncStore(cfg.StateDir, client.NodeID(), cfg.ControlURL)
	}
	store, e := nodeagent.OpenWGSyncStore(cfg.StateDir, client.NodeID(), cfg.ControlURL)
	if e != nil {
		return e
	}
	defer store.Close()
	// 默认关闭。只有部署方显式打开 NODE_WG_APPLY_ENABLED 时才真正改写内核 peer。
	var applier *nodeagent.WGApplier
	if cfg.ApplyEnabled {
		wg := &nodeagent.WGCommand{Binary: cfg.WGBinary}
		applier = &nodeagent.WGApplier{Clock: clk, NodeID: client.NodeID(), ManifestFile: store.ManifestPath(), Reader: wg, Writer: wg}
	}
	for {
		if ctx.Err() != nil {
			return store.Clear()
		}
		lease, e := client.FetchWireGuard(ctx)
		if e == nil {
			e = store.Publish(lease, clk.Now())
		}
		if e == nil && applier != nil {
			var applied nodeagent.WGApplyResult
			if applied, e = applier.Apply(ctx); e == nil && applied.Changed() {
				slog.Info("peer 已下发到内核", "added", applied.Added, "updated", applied.Updated, "removed", applied.Removed, "config_version", applied.ConfigVersion)
			}
		}
		if e != nil {
			if clear := store.Clear(); clear != nil {
				return clear
			}
			if ctx.Err() != nil {
				return nil
			}
			var delivery *nodeagent.DeliveryError
			if len(args) == 1 || !errors.As(e, &delivery) || delivery.Kind == "certificate_expired" || delivery.Kind == "invalid_snapshot" || (delivery.Code >= 300 && delivery.Code < 500 && delivery.Code != 429) {
				return errors.New("清单同步失败，旧清单已失效，请核实配置")
			}
			slog.Warn("清单暂时不可用，旧清单已失效")
		} else if len(args) == 1 {
			slog.Info("公开清单已同步，尚未应用到内核")
			return nil
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return store.Clear()
		case <-timer.C:
		}
	}
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if e := run(ctx, os.Args[1:]); e != nil {
		slog.Error("node-sync stopped", "err", safelog.Error(e))
		os.Exit(1)
	}
}
