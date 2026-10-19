package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// TrialDuration 是老板定的试用时长，按授予时刻起算的连续 24 小时，不是自然日。
	TrialDuration = 24 * time.Hour
	TrialTier     = 1
	TrialDevices  = 3
)

// TrialGrant 开通一次免费试用。幂等键固定为 trial:user:{user_id}。
type TrialGrant struct {
	UserID int64
}

// ApplyTrial 在调用方事务内写入试用权益。
// 调用方必须已经确认：账户没有其他权益流水、当前没有有效订阅、试用开关开着。
// 这里只负责「还没记过这笔流水就开通 24 小时」，重复调用直接返回已有到期时间，不加时。
func ApplyTrial(ctx context.Context, tx pgx.Tx, g TrialGrant, now time.Time) (RedeemResult, error) {
	var none RedeemResult
	if g.UserID <= 0 {
		return none, errors.New("invalid trial grant")
	}
	key := TrialIdempotencyKey(g.UserID)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscription_ledger WHERE idempotency_key = $1)`, key).Scan(&exists); err != nil {
		return none, err
	}
	before, err := Load(ctx, tx, g.UserID)
	if err != nil {
		return none, err
	}
	if exists {
		return resultFrom(before, false, true), nil
	}
	if before.Valid(now) {
		return none, errors.New("trial refused: subscription already active")
	}
	expire := now.Add(TrialDuration)
	after := before
	after.PlanID = nil
	after.Tier = TrialTier
	after.ExpireAt = &expire
	after.DeviceLimit = TrialDevices
	after.PeriodQuotaBytes = nil
	after.PeriodUsedBytes = 0
	after.PeriodResetAt = nil
	started := now
	after.StartedAt = &started
	if err := saveSubscription(ctx, tx, g.UserID, after); err != nil {
		return none, err
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_ledger (user_id, change_type, source_type, source_id, delta_seconds, delta_bytes, delta_devices,
		                                 before_snapshot, after_snapshot, idempotency_key, operator_type, operator_id, remark)
		VALUES ($1, 'TRIAL', 'TRIAL', $2, $3, 0, 0, $4, $5, $6, 'USER', $7, '免费试用 24 小时')`,
		g.UserID, g.UserID, int64(TrialDuration.Seconds()), b, a, key, g.UserID); err != nil {
		return none, err
	}
	return resultFrom(after, false, false), nil
}

// TrialIdempotencyKey 是一个账户唯一的试用流水键。
func TrialIdempotencyKey(userID int64) string {
	return "trial:user:" + strconv.FormatInt(userID, 10)
}
