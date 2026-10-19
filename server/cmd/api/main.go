// app-api：App 接口。节点心跳控制面由独立 cmd/node-api 提供。
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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/appinfo"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/connect"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/node"
	"github.com/yuhai-vpn/server/internal/order"
	"github.com/yuhai-vpn/server/internal/plan"
	"github.com/yuhai-vpn/server/internal/redeem"
	"github.com/yuhai-vpn/server/internal/redisx"
	"github.com/yuhai-vpn/server/internal/traffic"
	"github.com/yuhai-vpn/server/internal/trial"
	"github.com/yuhai-vpn/server/internal/tron"

	"github.com/yuhai-vpn/server/internal/safelog"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("配置错误，拒绝启动", "err", safelog.Error(err))
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("数据库连接失败", "err", safelog.Error(err))
		os.Exit(1)
	}
	defer pool.Close()

	rdb, err := redisx.New(cfg.RedisURL)
	if err != nil || rdb.Ping(ctx) != nil {
		slog.Error("Redis 连接失败", "err", safelog.Error(err))
		os.Exit(1)
	}
	enc, err := fieldenc.New(cfg.FieldEncKeyID, cfg.FieldEncKey)
	if err != nil {
		slog.Error("字段加密初始化失败", "err", safelog.Error(err))
		os.Exit(1)
	}
	var clk clock.Clock = clock.Real{}
	if !cfg.IsProduction() {
		clk = &clock.Adjustable{} // 非生产：允许 /admin/v1/dev/clock 拨动
	}
	signer := auth.NewSigner(cfg.JWTKeyID, cfg.JWTSeed)
	repo := &account.Repo{Pool: pool}
	mw := &auth.Middleware{Signer: signer, Store: repo, Redis: rdb, Clock: clk}
	accountSvc := &account.Service{Repo: repo, Signer: signer, Enc: enc, Redis: rdb, Clock: clk, Pepper: cfg.SecretPepper,
		WGPool: cfg.WGAddressPool, WGReserved: cfg.WGAddressReserved,
		Tron: &tron.TronGrid{APIKey: cfg.TronGridAPIKey}, ResetPayTo: cfg.PasswordResetPayTo}

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := buildRouter(routerDeps{Config: cfg, Pool: pool, Clock: clk, Account: accountSvc, MW: mw,
		InviteBaseURL: os.Getenv("INVITE_BASE_URL"), Cipher: enc})

	srv := &http.Server{Addr: cfg.APIListen, Handler: r, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		slog.Info("app-api listening", "addr", cfg.APIListen, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", safelog.Error(err))
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func notImplemented(c *gin.Context) { httpx.Fail(c, httpx.NewError(httpx.ErrNotFound, "尚未实现")) }

// routerDeps 把路由表与进程启动分开，好让「契约里有、代码里没挂上」这类缺口
// 能被测试直接查出来——PUT /v1/devices/current/wg-key 就是这么漏了整整一轮的。
type routerDeps struct {
	Config        config.Config
	Pool          *pgxpool.Pool
	Clock         clock.Clock
	Account       *account.Service
	MW            *auth.Middleware
	InviteBaseURL string
	// Cipher 解密出口凭据；测试里可为 nil（出口下发随之拒绝）。
	Cipher *fieldenc.Cipher
}

func buildRouter(d routerDeps) *gin.Engine {
	r := gin.New()
	// 默认仅使用直连客户端地址；未经显式信任的转发头不能改变限流身份。
	_ = r.SetTrustedProxies(httpx.TrustedProxiesFromEnv())
	r.Use(httpx.SecurityHeaders(d.Config.IsProduction()), httpx.RequestIDMiddleware(), httpx.RecoveryMiddleware(), httpx.AccessLogMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// S0
	r.GET("/app/version", (&appinfo.Handler{Svc: &appinfo.Service{Pool: d.Pool, Clock: d.Clock}}).GetVersion)
	v1 := r.Group("/v1")
	v1.GET("/bootstrap", notImplemented)
	(&account.Handler{Svc: d.Account, MW: d.MW}).Mount(v1)
	(&plan.Handler{Svc: &plan.Service{Pool: d.Pool, Clock: d.Clock}, MW: d.MW}).Mount(v1)
	(&order.Handler{Svc: &order.Service{Pool: d.Pool, Clock: d.Clock, Production: d.Config.IsProduction()}, MW: d.MW}).Mount(v1)
	(&node.Handler{Svc: &node.ListService{Pool: d.Pool, Clock: d.Clock}, MW: d.MW}).Mount(v1)
	appInviteHandler(d.Pool, d.Clock, d.MW, d.Config.InviteRewardsEnabled, d.InviteBaseURL).Mount(v1)
	(&traffic.Handler{Svc: &traffic.Service{Pool: d.Pool, Clock: d.Clock}, MW: d.MW}).Mount(v1)
	(&connect.Handler{Svc: &connect.Service{Pool: d.Pool, Clock: d.Clock, Cipher: d.Cipher}, MW: d.MW}).Mount(v1)
	(&redeem.Handler{Svc: &redeem.Service{Pool: d.Pool, Clock: d.Clock}, MW: d.MW}).Mount(v1)
	(&trial.Handler{Svc: &trial.Service{Pool: d.Pool, Clock: d.Clock}, MW: d.MW}).Mount(v1)
	return r
}
