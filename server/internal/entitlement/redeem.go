package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// RedeemGrant 是一张兑换码上的权益快照。不读 plans 表。
// 幂等键固定为 redeem:{code_id}，同一张码即使并发也只会记一笔流水。
type RedeemGrant struct {
	UserID       int64
	CodeID       int64
	DurationDays int
	Tier         int
	DeviceLimit  int
	// QuotaBytes 为 nil 表示不限流量。
	QuotaBytes *int64
	Remark     string
}

// RedeemResult 是开通之后的订阅状态。
//
// 叠加规则（有未过期权益时）：
//   - 到期时间 = 当前到期时间 + 卡的天数。从当前到期往后续，不用「从现在起算」覆盖一段更长的剩余时间。
//   - 等级取较高者，不把已有的更高等级降成卡的等级。
//   - 设备数取较大者，不把已有的更多设备改少。
//   - 流量：不限（nil）比有限额度更宽。卡是不限则结果为不限；原来已是不限则保持不限；两边都有限额度时取较大的额度。不把流量改小。
//   - 只有到期时间在变、等级 / 设备 / 流量都没变时，保留原来的 plan_id（付费套餐名称还在）。
//     只要等级、设备或流量被抬高，就清掉 plan_id，避免用旧套餐名描述已经被兑换码改过的条件。界面会显示「兑换卡」。
//
// 没有有效权益时（没有订阅、已过期、等级为 0）：按卡本身开通，从现在起算，不从已经过去的到期日往上加。
// 已过期的更高等级不会被保留——那段权益已经结束，不是「缩短」。
type RedeemResult struct {
	ExpireAt    time.Time
	Tier        int
	DeviceLimit int
	Unlimited   bool
	Extended    bool
	// AlreadyApplied 表示这笔流水以前就写过了（幂等重入），本次没有再次加天数。
	AlreadyApplied bool
}

// ApplyRedeem 在调用方事务内开通兑换码权益。
func ApplyRedeem(ctx context.Context, tx pgx.Tx, g RedeemGrant, now time.Time) (RedeemResult, error) {
	var none RedeemResult
	if g.UserID <= 0 || g.CodeID <= 0 || g.DurationDays <= 0 || g.DurationDays > 3650 ||
		g.Tier < 1 || g.Tier > 9 || g.DeviceLimit <= 0 || g.DeviceLimit > 100 {
		return none, errors.New("invalid redeem grant")
	}
	if g.QuotaBytes != nil && *g.QuotaBytes <= 0 {
		return none, errors.New("invalid redeem quota")
	}
	key := "redeem:" + strconv.FormatInt(g.CodeID, 10)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscription_ledger WHERE idempotency_key = $1)`, key).Scan(&exists); err != nil {
		return none, err
	}
	before, err := Load(ctx, tx, g.UserID)
	if err != nil {
		return none, err
	}
	if exists {
		return resultFrom(before, before.Valid(now), true), nil
	}

	after := before
	active := before.Valid(now)
	dur := time.Duration(g.DurationDays) * 24 * time.Hour
	var newExpire time.Time
	if active {
		newExpire = before.ExpireAt.Add(dur)
	} else {
		newExpire = now.Add(dur)
	}
	after.ExpireAt = &newExpire

	tier, devices := g.Tier, g.DeviceLimit
	quota := g.QuotaBytes
	if active {
		if before.Tier > tier {
			tier = before.Tier
		}
		if before.DeviceLimit > devices {
			devices = before.DeviceLimit
		}
		quota = mergeQuota(before.PeriodQuotaBytes, g.QuotaBytes)
	}
	after.Tier = tier
	after.DeviceLimit = devices
	material := !active || tier != before.Tier || devices != before.DeviceLimit || quotaChanged(before.PeriodQuotaBytes, quota)
	if material {
		after.PlanID = nil
	}
	if !active {
		started := now
		after.StartedAt = &started
		after.PeriodUsedBytes = 0
	}
	after.PeriodQuotaBytes = quota
	if quota == nil {
		after.PeriodResetAt = nil
	} else if !active || before.PeriodQuotaBytes == nil {
		reset := now.Add(30 * 24 * time.Hour)
		after.PeriodResetAt = &reset
		if !active {
			after.PeriodUsedBytes = 0
		}
	}
	if err := saveSubscription(ctx, tx, g.UserID, after); err != nil {
		return none, err
	}

	var dSec int64
	if active {
		dSec = int64(newExpire.Sub(*before.ExpireAt).Seconds())
	} else {
		dSec = int64(dur.Seconds())
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_ledger (user_id, change_type, source_type, source_id, delta_seconds, delta_bytes, delta_devices,
		                                 before_snapshot, after_snapshot, idempotency_key, operator_type, operator_id, remark)
		VALUES ($1, 'REDEEM', 'REDEEM_CODE', $2, $3, 0, 0, $4, $5, $6, 'USER', $7, NULLIF($8, ''))`,
		g.UserID, g.CodeID, dSec, b, a, key, g.UserID, g.Remark); err != nil {
		return none, err
	}
	return resultFrom(after, active, false), nil
}

func mergeQuota(current, card *int64) *int64 {
	if card == nil || current == nil {
		return nil
	}
	if *card > *current {
		return card
	}
	return current
}

func quotaChanged(before, after *int64) bool {
	if before == nil || after == nil {
		return before != nil || after != nil
	}
	return *before != *after
}

func resultFrom(s Subscription, extended, already bool) RedeemResult {
	out := RedeemResult{Tier: s.Tier, DeviceLimit: s.DeviceLimit, Unlimited: s.PeriodQuotaBytes == nil, Extended: extended, AlreadyApplied: already}
	if s.ExpireAt != nil {
		out.ExpireAt = *s.ExpireAt
	}
	return out
}
