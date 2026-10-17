// admin-api：管理后台接口，仅管理区可达。开发工具路由仅在非生产且 DEV_TOOLS_ENABLED 时注册。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/admin"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/deposit"
	"github.com/yuhai-vpn/server/internal/egress"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"

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

	if cfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	r, err := buildAdminRouter(cfg, pool)
	if err != nil {
		slog.Error("后台路由初始化失败", "err", safelog.Error(err))
		os.Exit(1)
	}

	srv := &http.Server{Addr: cfg.AdminListen, Handler: r, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		slog.Info("admin-api listening", "addr", cfg.AdminListen, "env", cfg.Env)
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

// buildAdminRouter 把后台路由表与进程启动分开，好让「契约里有、代码里没挂上」
// 这类缺口能被测试直接查出来（见 routes_test.go）。
func buildAdminRouter(cfg config.Config, pool *pgxpool.Pool) (*gin.Engine, error) {
	r := gin.New()
	_ = r.SetTrustedProxies(httpx.TrustedProxiesFromEnv())
	r.Use(httpx.SecurityHeaders(cfg.IsProduction()), httpx.RequestIDMiddleware(), httpx.RecoveryMiddleware(), httpx.AccessLogMiddleware())
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	enc, err := fieldenc.New(cfg.FieldEncKeyID, cfg.FieldEncKey)
	if err != nil {
		return nil, fmt.Errorf("后台密钥配置错误: %w", err)
	}
	adminSvc, err := admin.New(pool, clock.Real{}, enc)
	if err != nil {
		return nil, fmt.Errorf("后台认证初始化失败: %w", err)
	}
	adminHandler := &admin.Handler{Service: adminSvc, SecureCookies: cfg.Env != config.EnvDevelopment}
	adminGroup := r.Group("/admin/v1")
	adminHandler.Register(adminGroup)
	auditSvc := &audit.Service{Pool: pool, Clock: clock.Real{}}
	approvalSvc := &admin.ApprovalService{Auth: adminSvc, Audit: auditSvc,
		FundPubKey: cfg.FundAddrPubKey}
	(&admin.AdminManagementHandler{Auth: adminHandler, Service: &admin.AdminManagementService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.ApprovalHandler{Auth: adminHandler, Service: approvalSvc, Audit: auditSvc}).Register(adminGroup)
	(&admin.VersionHandler{Auth: adminHandler, Service: &admin.VersionService{Approvals: approvalSvc}}).Register(adminGroup)
	(&admin.UserReadHandler{Auth: adminHandler, Service: &admin.UserReadService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.UserDevicesHandler{Auth: adminHandler, Service: &admin.UserReadService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.NodeReadHandler{Auth: adminHandler, Service: &admin.NodeReadService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.DeviceRevokeHandler{Auth: adminHandler, Service: &admin.DeviceRevokeService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.EgressHandler{Auth: adminHandler, Service: &admin.EgressService{Auth: adminSvc, Audit: auditSvc,
		Cipher: enc, Fetcher: egress.NewFetcher()}}).Register(adminGroup)
	(&admin.NodeWriteHandler{Auth: adminHandler, Service: &admin.NodeWriteService{Auth: adminSvc, Audit: auditSvc},
		Read: &admin.NodeReadService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.NodeEnrollHandler{Auth: adminHandler, Service: &admin.NodeEnrollService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.FinanceReadHandler{Auth: adminHandler, Service: &admin.FinanceReadService{Auth: adminSvc},
		Exceptions: &admin.DepositExceptionService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.RiskHandler{Auth: adminHandler, Read: &admin.RiskReadService{Auth: adminSvc},
		Write: &admin.RiskWriteService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.InviteHandler{Auth: adminHandler, Read: &admin.InviteReadService{Auth: adminSvc},
		Review: &admin.InviteReviewService{Security: approvalSvc},
		Rules:  &admin.InviteRuleWriteService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.PlanHandler{Auth: adminHandler, Read: &admin.PlanReadService{Auth: adminSvc},
		Write: &admin.PlanWriteService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.TrafficReadHandler{Auth: adminHandler,
		Service: &admin.TrafficReadService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.DashboardHandler{Auth: adminHandler,
		Service: &admin.DashboardService{Auth: adminSvc}}).Register(adminGroup)
	(&admin.ConfigHandler{Auth: adminHandler,
		Write: &admin.ConfigWriteService{Security: approvalSvc}}).Register(adminGroup)
	(&admin.ExportHandler{Auth: adminHandler, Service: &admin.ExportService{
		Security: approvalSvc, Read: &admin.FinanceReadService{Auth: adminSvc}}}).Register(adminGroup)
	(&admin.FinanceAddressHandler{Auth: adminHandler,
		Read:   &admin.FinanceReadService{Auth: adminSvc},
		Write:  &admin.FinanceAddressService{Security: approvalSvc},
		Import: &admin.FinanceImportService{Security: approvalSvc, FundPubKey: cfg.FundAddrPubKey}}).Register(adminGroup)

	if !cfg.IsProduction() && cfg.DevToolsEnabled {
		depositSvc := &deposit.Service{Pool: pool, Clock: clock.Real{}, Production: false}
		dev := adminGroup.Group("/dev", adminHandler.Middleware(), admin.RequirePermission("dev.tools"))
		// 开发工具同样要求有效后台会话、CSRF 和 dev.tools 权限。
		dev.POST("/mock-deposit", func(c *gin.Context) {
			var in deposit.MockInput
			if err := c.ShouldBindJSON(&in); err != nil {
				httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
				return
			}
			res, err := depositSvc.MockDeposit(c.Request.Context(), in)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, res)
		})
		dev.GET("/reconcile", func(c *gin.Context) {
			r, err := depositSvc.Reconcile(c.Request.Context())
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, r)
		})
		dev.POST("/mock-traffic", notImplemented) // 第 9–10 周
		dev.POST("/clock", notImplemented)        // 第 9–10 周：偏移量存 Redis，各进程共享
		slog.Warn("开发工具路由已注册（非生产环境）")
	}
	return r, nil
}
