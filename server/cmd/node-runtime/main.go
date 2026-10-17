// node-runtime observes existing WireGuard state; it never changes networking.
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
	if len(args) > 1 || (len(args) == 1 && args[0] != "once" && args[0] != "init-state") {
		return errors.New("只支持 init-state、once 或无参数常驻运行")
	}
	cfg, e := config.LoadNodeRuntime()
	if e != nil {
		return e
	}
	if !cfg.Enabled {
		slog.Info("WireGuard运行核对未启用")
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("真实WireGuard核对只支持Linux")
	}
	if len(args) == 1 && args[0] == "init-state" {
		return nodeagent.InitRuntimeStore(cfg.RuntimeDir, cfg.NodeID)
	}
	clk := clock.Real{}
	observer := &nodeagent.WGObserver{NodeID: cfg.NodeID, ManifestFile: cfg.ManifestFile, Clock: clk, Reader: &nodeagent.WGCommand{Binary: cfg.Binary}}
	store, e := nodeagent.OpenRuntimeStore(cfg.RuntimeDir, cfg.NodeID)
	if e != nil {
		return e
	}
	defer store.Close()
	for {
		if ctx.Err() != nil {
			return nil
		}
		status, e := observer.Observe(ctx)
		if e != nil {
			if err := store.Clear(); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("WireGuard核对未通过，候选状态已失效")
			if len(args) == 1 {
				return errors.New("本次核对未通过")
			}
		} else {
			if e = store.Publish(status, clk.Now()); e != nil {
				if clearErr := store.Clear(); clearErr != nil {
					return clearErr
				}
				return errors.New("不能保存运行核对结果")
			}
			if len(args) == 1 {
				slog.Info("WireGuard公开配置核对通过")
				return nil
			}
		}
		timer := time.NewTimer(3 * time.Second)
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
	if e := run(ctx, os.Args[1:]); e != nil {
		slog.Error("node-runtime stopped", "err", safelog.Error(e))
		os.Exit(1)
	}
}
