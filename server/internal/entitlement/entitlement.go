// Package entitlement 实现权益开通（方案 5.2–5.3）：时长套餐、流量包、设备加购的写入与只追加流水 subscription_ledger。
// 所有入口在调用方事务内执行，并以幂等键去重（fulfill:order:{id} / invite_reward:{id} / admin_adjust:approval:{id}）。
package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Plan 是套餐快照（下单时写入 orders.plan_snapshot，开通时只用快照，不再读 plans 表）。
type Plan struct {
	ID               int64  `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	Type             string `json:"plan_type"` // TIME / TRAFFIC / DEVICE_ADDON
	Tier             int    `json:"tier"`
	DurationDays     int    `json:"duration_days,omitempty"`
	PeriodQuotaBytes *int64 `json:"period_quota_bytes,omitempty"`
	PackBytes        int64  `json:"pack_bytes,omitempty"`
	ValidityDays     int    `json:"validity_days,omitempty"`
	DeviceLimit      int    `json:"device_limit,omitempty"`
	ExtraDevices     int    `json:"extra_devices,omitempty"`
	PriceMicro       int64  `json:"price_micro"`
	Version          int    `json:"version"`
}

type Subscription struct {
	PlanID           *int64     `json:"plan_id"`
	Tier             int        `json:"tier"`
	StartedAt        *time.Time `json:"started_at"`
	ExpireAt         *time.Time `json:"expire_at"`
	DeviceLimit      int        `json:"device_limit"`
	PeriodQuotaBytes *int64     `json:"period_quota_bytes"`
	PeriodUsedBytes  int64      `json:"period_used_bytes"`
	PeriodResetAt    *time.Time `json:"period_reset_at"`
}

func (s Subscription) Valid(now time.Time) bool {
	return s.Tier > 0 && s.ExpireAt != nil && s.ExpireAt.After(now)
}

type Grant struct {
	UserID     int64
	Plan       Plan
	ChangeType string // NEW / RENEW / TIER_CHANGE / TRAFFIC_PACK / ADDON
	SourceType string // ORDER / INVITE_REWARD / ADMIN_APPROVAL / SYSTEM
	SourceID   int64
	IdemKey    string
	Operator   string
	Remark     string
}

var ErrNoActivePlan = errors.New("no active time plan for addon")

// Load 读取并锁定用户当前订阅（事务内）。
func Load(ctx context.Context, tx pgx.Tx, userID int64) (Subscription, error) {
	var s Subscription
	err := tx.QueryRow(ctx, `
		SELECT plan_id, tier, started_at, expire_at, device_limit, period_quota_bytes, period_used_bytes, period_reset_at
		  FROM subscriptions WHERE user_id = $1 FOR UPDATE`, userID).
		Scan(&s.PlanID, &s.Tier, &s.StartedAt, &s.ExpireAt, &s.DeviceLimit, &s.PeriodQuotaBytes, &s.PeriodUsedBytes, &s.PeriodResetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `INSERT INTO subscriptions (user_id) VALUES ($1)`, userID); err != nil {
			return s, err
		}
		return Load(ctx, tx, userID)
	}
	return s, err
}

// Apply 开通权益。幂等：subscription_ledger.idempotency_key 已存在则直接返回 nil。
// 返回 ErrNoActivePlan 表示加购条件不满足（调用方转余额，方案 5.3）。
func Apply(ctx context.Context, tx pgx.Tx, g Grant, now time.Time) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscription_ledger WHERE idempotency_key = $1)`, g.IdemKey).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	before, err := Load(ctx, tx, g.UserID)
	if err != nil {
		return err
	}
	after := before
	var dSec, dBytes int64
	var dDev int
	changeType := g.ChangeType

	switch g.Plan.Type {
	case "TIME":
		dur := time.Duration(g.Plan.DurationDays) * 24 * time.Hour
		base := now
		if before.Valid(now) && before.Tier == g.Plan.Tier {
			base = *before.ExpireAt // 同等级续费：max(now, 原到期) + 时长
			changeType = "RENEW"
		} else if before.Valid(now) {
			changeType = "TIER_CHANGE" // 跨等级：折算已在报价金额中体现（方案 11.4），到期时间从开通时刻起算
		} else {
			changeType = "PURCHASE"
		}
		newExpire := base.Add(dur)
		if before.ExpireAt != nil && before.Valid(now) {
			dSec = int64(newExpire.Sub(*before.ExpireAt).Seconds())
		} else {
			dSec = int64(dur.Seconds())
		}
		after.PlanID, after.Tier, after.ExpireAt, after.DeviceLimit = &g.Plan.ID, g.Plan.Tier, &newExpire, g.Plan.DeviceLimit
		if changeType != "RENEW" {
			started := now
			reset := now.Add(30 * 24 * time.Hour)
			after.StartedAt, after.PeriodQuotaBytes, after.PeriodUsedBytes, after.PeriodResetAt = &started, g.Plan.PeriodQuotaBytes, 0, &reset
			if g.Plan.PeriodQuotaBytes == nil {
				after.PeriodResetAt = nil
			}
		}
		if err := saveSubscription(ctx, tx, g.UserID, after); err != nil {
			return err
		}
	case "TRAFFIC":
		changeType = "TRAFFIC_PACK"
		dBytes = g.Plan.PackBytes
		expire := now.Add(time.Duration(g.Plan.ValidityDays) * 24 * time.Hour)
		if _, err := tx.Exec(ctx, `
			INSERT INTO traffic_packs (user_id, tier, device_limit, total_bytes, expire_at, source_type, source_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (source_type, source_id) DO NOTHING`,
			g.UserID, g.Plan.Tier, g.Plan.DeviceLimit, g.Plan.PackBytes, expire, g.SourceType, g.SourceID); err != nil {
			return err
		}
	case "DEVICE_ADDON":
		if !before.Valid(now) {
			return ErrNoActivePlan
		}
		changeType = "DEVICE_ADDON"
		dDev = g.Plan.ExtraDevices
		if _, err := tx.Exec(ctx, `
			INSERT INTO device_addons (user_id, quantity, expire_at, source_type, source_id)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT (source_type, source_id) DO NOTHING`,
			g.UserID, g.Plan.ExtraDevices, *before.ExpireAt, g.SourceType, g.SourceID); err != nil {
			return err
		}
	default:
		return errors.New("unknown plan type " + g.Plan.Type)
	}

	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	_, err = tx.Exec(ctx, `
		INSERT INTO subscription_ledger (user_id, change_type, source_type, source_id, delta_seconds, delta_bytes, delta_devices,
		                                 before_snapshot, after_snapshot, idempotency_key, operator_type, remark)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, ''))`,
		g.UserID, changeType, g.SourceType, g.SourceID, dSec, dBytes, dDev, b, a, g.IdemKey, g.Operator, g.Remark)
	return err
}

func saveSubscription(ctx context.Context, tx pgx.Tx, userID int64, s Subscription) error {
	_, err := tx.Exec(ctx, `
		UPDATE subscriptions SET plan_id = $2, tier = $3, started_at = $4, expire_at = $5, device_limit = $6,
		       period_quota_bytes = $7, period_used_bytes = $8, period_reset_at = $9, version = version + 1, updated_at = now()
		 WHERE user_id = $1`,
		userID, s.PlanID, s.Tier, s.StartedAt, s.ExpireAt, s.DeviceLimit, s.PeriodQuotaBytes, s.PeriodUsedBytes, s.PeriodResetAt)
	return err
}
