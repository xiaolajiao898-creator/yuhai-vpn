package invite

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/balance"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/money"
)

func grantReward(ctx context.Context, tx pgx.Tx, r pendingReward, snap rewardSnapshot, key string, now time.Time) (string, error) {
	if r.Kind == "BALANCE" {
		_, err := balance.Apply(ctx, tx, balance.Entry{UserID: r.Beneficiary, EntryType: "INVITE_REWARD", Amount: money.Micro(r.Value), SourceType: "INVITE_REWARD", SourceID: r.ID, IdemKey: key, Operator: "SYSTEM", Remark: "邀请奖励"})
		if errors.Is(err, balance.ErrOverflow) {
			return "BALANCE_LIMIT_REVIEW", nil
		}
		return "", err
	}
	before, err := entitlement.Load(ctx, tx, r.Beneficiary)
	if err != nil {
		return "", err
	}
	after := before
	var seconds, bytes int64
	switch r.Kind {
	case "DAYS":
		if r.Value <= 0 || r.Value > 36500 {
			return "REWARD_VALUE_INVALID", nil
		}
		base := now
		if before.Valid(now) {
			base = *before.ExpireAt
		} else {
			if !validRewardPlan(snap.GrantPlan, "DAYS") {
				return "REWARD_PLAN_REQUIRED", nil
			}
			p := snap.GrantPlan
			reset := now.Add(30 * 24 * time.Hour)
			after.PlanID, after.Tier, after.DeviceLimit = &p.ID, p.Tier, p.DeviceLimit
			after.StartedAt, after.PeriodQuotaBytes, after.PeriodUsedBytes, after.PeriodResetAt = &now, p.PeriodQuotaBytes, 0, &reset
			if p.PeriodQuotaBytes == nil {
				after.PeriodResetAt = nil
			}
		}
		seconds = r.Value * 86400
		expires := base.Add(time.Duration(seconds) * time.Second)
		if expires.Year() > 9999 || !expires.After(base) {
			return "REWARD_TIME_RANGE", nil
		}
		after.ExpireAt = &expires
		if _, err = tx.Exec(ctx, `UPDATE subscriptions SET plan_id=$2,tier=$3,started_at=$4,expire_at=$5,device_limit=$6,period_quota_bytes=$7,period_used_bytes=$8,period_reset_at=$9,version=version+1,updated_at=$10 WHERE user_id=$1`, r.Beneficiary, after.PlanID, after.Tier, after.StartedAt, after.ExpireAt, after.DeviceLimit, after.PeriodQuotaBytes, after.PeriodUsedBytes, after.PeriodResetAt, now); err != nil {
			return "", err
		}
	case "TRAFFIC":
		if !validRewardPlan(snap.GrantPlan, "TRAFFIC") || r.Value <= 0 || r.Value > 1<<53-1 {
			return "REWARD_PLAN_REQUIRED", nil
		}
		p := snap.GrantPlan
		expires := now.Add(time.Duration(p.ValidityDays) * 24 * time.Hour)
		if expires.Year() > 9999 {
			return "REWARD_TIME_RANGE", nil
		}
		bytes = r.Value
		tag, err := tx.Exec(ctx, `INSERT INTO traffic_packs(user_id,tier,device_limit,total_bytes,expire_at,source_type,source_id,created_at) VALUES($1,$2,$3,$4,$5,'INVITE_REWARD',$6,$7) ON CONFLICT(source_type,source_id) DO NOTHING`, r.Beneficiary, p.Tier, p.DeviceLimit, bytes, expires, r.ID, now)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() != 1 {
			return "PREEXISTING_TRAFFIC_PACK", nil
		}
	default:
		return "REWARD_TYPE_UNSUPPORTED", nil
	}
	b, err := json.Marshal(before)
	if err != nil {
		return "", err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO subscription_ledger(user_id,change_type,source_type,source_id,delta_seconds,delta_bytes,before_snapshot,after_snapshot,idempotency_key,operator_type,remark,created_at) VALUES($1,'INVITE_REWARD','INVITE_REWARD',$2,$3,$4,$5,$6,$7,'SYSTEM','邀请奖励',$8)`, r.Beneficiary, r.ID, seconds, bytes, b, a, key, now); err != nil {
		return "", err
	}
	// Persist a control-plane event in the same commit so newly granted service
	// can be synchronized by a node worker without relying on browser activity.
	_, err = tx.Exec(ctx, `INSERT INTO node_user_events(credential_id,effective_tier,reason,created_at)
	 SELECT c.id,GREATEST(COALESCE((SELECT tier FROM subscriptions WHERE user_id=$1 AND expire_at>$2),0),COALESCE((SELECT max(tier) FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes),0)),'INVITE_REWARD',$2
	 FROM device_credentials c JOIN devices d ON d.id=c.device_id WHERE d.user_id=$1 AND d.status='ACTIVE' AND c.status='ACTIVE'`, r.Beneficiary, now)
	return "", err
}
