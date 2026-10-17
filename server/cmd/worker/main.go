// worker：链上扫描、到账、订单过期、订阅到期、周期重置、奖励释放、outbox 投递、每日对账。无入站端口。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/admin"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/config"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/deposit"
	"github.com/yuhai-vpn/server/internal/invite"
	"github.com/yuhai-vpn/server/internal/nodecontrol"
	"github.com/yuhai-vpn/server/internal/order"
	"github.com/yuhai-vpn/server/internal/outbox"

	"github.com/yuhai-vpn/server/internal/safelog"
)

// 任务表（间隔见方案 6.4、8.5、7.4、12.3）。每个任务必须可重入。
type job struct {
	name     string
	interval time.Duration
	run      func(ctx context.Context, now time.Time) error // nil = 尚未实现
}

var schedule = []job{
	{"chain_scan", 10 * time.Second, nil},         // 阶段二：TronGrid 增量扫描
	{"deposit_settle", 5 * time.Second, nil},      // 处理 chain_transfers 中未结算记录
	{"order_expire", 30 * time.Second, nil},       // PENDING 到期 → EXPIRED
	{"subscription_expire", 1 * time.Minute, nil}, // expire:{user_id}:{unix}
	{"period_reset", 1 * time.Minute, nil},        // period_reset:{user_id}:{unix}
	{"outbox_dispatch", 2 * time.Second, nil},     // 领域事件投递（租约式领取）
	{"traffic_rollup", 5 * time.Minute, nil},      // traffic_reports → traffic_daily
	{"ip_hash_purge", 1 * time.Hour, nil},         // 30 天后置空 register_ip_*
	{"daily_reconcile", 24 * time.Hour, nil},      // 方案 10.5 对账查询
}

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

	clk := clock.Real{}
	accountJobs := &account.Jobs{Repo: &account.Repo{Pool: pool}}
	exportJobs := &admin.ExportJobs{Pool: pool, Clock: clk}
	// 默认关闭。只在显式打开时才为已收敛的节点登记撤销执行回执；不改变任何节点状态。
	receipts := &nodecontrol.RemovalReceipts{Pool: pool, Clock: clk, Enabled: cfg.RemovalReceiptsEnabled}
	jobs := append(append([]job(nil), schedule...),
		job{"account_secret_purge", time.Minute, accountJobs.PurgeAccountSecrets},
		job{"session_purge", time.Hour, accountJobs.PurgeExpiredSessions},
		// 后台导出：生成待办的，并执行方案 12.4 的 24 小时保留期。
		job{"export_build", 10 * time.Second, exportJobs.RunExports},
		job{"export_purge", time.Minute, exportJobs.PurgeExports},
		job{"removal_receipts", 30 * time.Second, func(c context.Context, _ time.Time) error {
			n, e := receipts.ConfirmOnce(c)
			if e == nil && n > 0 {
				slog.Info("撤销已在节点确认生效", "receipts", n)
			}
			return e
		}},
	)
	orderSvc := &order.Service{Pool: pool, Clock: clk}
	depositSvc := &deposit.Service{Pool: pool, Clock: clk, Production: cfg.IsProduction()}
	rewardJobs, rewardHandlers, err := wireInviteRewards(cfg.InviteRewardsEnabled, &invite.RewardService{Pool: pool, Clock: clk})
	if err != nil {
		slog.Error("邀请奖励任务配置错误，拒绝启动", "err", safelog.Error(err))
		os.Exit(1)
	}
	jobs = append(jobs, rewardJobs...)
	// When the reward gate is closed, ORDER_COMPLETED is not registered; the
	// dispatcher leaves these events pending, including pre-existing events.
	dispatcher := &outbox.Dispatcher{Pool: pool, Handlers: rewardHandlers}
	for i := range jobs {
		switch jobs[i].name {
		case "ip_hash_purge":
			jobs[i].run = accountJobs.PurgeIPHashes
		case "order_expire":
			jobs[i].run = orderSvc.ExpirePending
		case "deposit_settle":
			jobs[i].run = depositSvc.SettlePending
		case "outbox_dispatch":
			jobs[i].run = dispatcher.Run
		case "daily_reconcile":
			jobs[i].run = func(ctx context.Context, _ time.Time) error {
				r, err := depositSvc.Reconcile(ctx)
				if err != nil {
					return err
				}
				if !r.OK {
					slog.Error("对账不一致，需人工核查", "report", r) // 接入告警后：冻结相关用户余额使用
				}
				return nil
			}
		}
	}

	for _, j := range jobs {
		go func(j job) {
			t := time.NewTicker(j.interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if j.run == nil {
						continue
					}
					start := time.Now()
					if err := j.run(ctx, clk.Now()); err != nil {
						slog.Error("job failed", "job", j.name, "err", safelog.Error(err))
					} else {
						slog.Debug("job done", "job", j.name, "ms", time.Since(start).Milliseconds())
					}
				}
			}
		}(j)
	}
	slog.Info("worker started", "jobs", len(jobs), "env", cfg.Env)
	<-ctx.Done()
}
