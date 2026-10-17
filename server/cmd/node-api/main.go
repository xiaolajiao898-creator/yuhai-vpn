// node-api is a separately gated, private mTLS process. It cannot start tunnels,
// enroll certificates, decrypt user secrets, or invoke any payment worker.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/nodecontrol"

	"github.com/yuhai-vpn/server/internal/safelog"
)

func run(ctx context.Context) error {
	cfg, err := config.LoadNodeAPI()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Info("节点控制面未启用")
		return nil
	}
	if cfg.Env == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("节点数据库连接失败")
	}
	defer pool.Close()
	srv, err := nodecontrol.NewServer(cfg.Listen, cfg.ServerCert, cfg.ServerKey, cfg.ClientCA, pool, clock.Real{}, nodecontrol.ServerOptions{WireGuardManifestEnabled: cfg.WireGuardManifestEnabled, TrafficReportEnabled: cfg.TrafficReportEnabled,
		EnrollCACert: cfg.EnrollCACert, EnrollCAKey: cfg.EnrollCAKey})
	if err != nil {
		return err
	}
	// Missing migration must fail startup, not run a listener that cannot persist.
	if _, err = pool.Exec(ctx, `SELECT node_id,sequence,cert_fingerprint,ready_protocols FROM node_heartbeats LIMIT 0`); err != nil {
		return errors.New("节点心跳数据库迁移或读取权限未就绪")
	}
	done := make(chan error, 1)
	go func() {
		slog.Info("node-api mTLS listening", "addr", cfg.Listen)
		done <- srv.ListenAndServeTLS("", "")
	}()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err = srv.Shutdown(shutdown); err != nil {
			_ = srv.Close()
			return err
		}
		err = <-done
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("节点控制面停止", "err", safelog.Error(err))
		os.Exit(1)
	}
}
