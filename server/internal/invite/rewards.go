package invite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/outbox"
)

// RewardPool accepts a production pool or a transaction/savepoint in tests.
type RewardPool interface {
	Begin(context.Context) (pgx.Tx, error)
}
type RewardService struct {
	Pool  RewardPool
	Clock clock.Clock
}

type rewardRule struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Trigger     string     `json:"trigger_event"`
	Beneficiary string     `json:"beneficiary"`
	Kind        string     `json:"reward_type"`
	Value       int64      `json:"reward_value"`
	Percent     *int       `json:"reward_percent_bp"`
	Minimum     *int64     `json:"min_order_amount_micro"`
	Freeze      int        `json:"freeze_hours"`
	Cap         *int       `json:"daily_cap_per_inviter"`
	Version     int        `json:"version"`
	Starts      *time.Time `json:"starts_at"`
	Ends        *time.Time `json:"ends_at"`
}
type rewardSnapshot struct {
	Version     int               `json:"snapshot_version"`
	Rule        rewardRule        `json:"rule"`
	OrderID     int64             `json:"order_id"`
	OrderBase   int64             `json:"order_base_micro"`
	OrderPay    int64             `json:"order_pay_micro"`
	PaidAt      *time.Time        `json:"paid_at"`
	CompletedAt *time.Time        `json:"completed_at"`
	Value       int64             `json:"calculated_value"`
	GrantPlan   *entitlement.Plan `json:"grant_plan,omitempty"`
}
type rewardOrder struct {
	ID, UserID, Base, Pay             int64
	Status, Method, Kind, Fulfillment string
	PaidAt, CompletedAt               *time.Time
	TransferID                        *int64
}
type rewardRelation struct {
	Inviter, Invitee int64
	Code             string
	Created          time.Time
	Flags            []byte
}
type pendingReward struct {
	ID, RuleID, Inviter, Invitee, Beneficiary, Value int64
	Kind, Ref                                        string
	Snapshot                                         []byte
}

func rewardTransaction(ctx context.Context, pool RewardPool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func loadRewardOrder(ctx context.Context, tx pgx.Tx, id int64) (rewardOrder, error) {
	o := rewardOrder{ID: id}
	err := tx.QueryRow(ctx, `SELECT user_id,base_amount_micro,pay_amount_micro,status,pay_method,order_type,COALESCE(fulfillment_result,''),paid_at,completed_at,chain_transfer_id FROM orders WHERE id=$1 FOR SHARE`, id).Scan(&o.UserID, &o.Base, &o.Pay, &o.Status, &o.Method, &o.Kind, &o.Fulfillment, &o.PaidAt, &o.CompletedAt, &o.TransferID)
	return o, err
}
func loadRewardRelation(ctx context.Context, tx pgx.Tx, invitee int64) (rewardRelation, error) {
	r := rewardRelation{Invitee: invitee}
	err := tx.QueryRow(ctx, `SELECT inviter_user_id,invite_code,created_at,risk_flags FROM invite_relations WHERE invitee_user_id=$1 FOR SHARE`, invitee).Scan(&r.Inviter, &r.Code, &r.Created, &r.Flags)
	return r, err
}

// OnOrderCompleted ignores payload amounts/identities entirely. Unknown or
// uncompleted orders return errors so the durable outbox is not acknowledged.
func (s *RewardService) OnOrderCompleted(ctx context.Context, ev outbox.Event) error {
	if ev.Type != outbox.OrderCompleted || ev.AggregateType != "order" || ev.AggregateID <= 0 {
		return errors.New("invalid completed-order event")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	now := s.Clock.Now().UTC()
	return rewardTransaction(ctx, s.Pool, func(tx pgx.Tx) error {
		o, err := loadRewardOrder(ctx, tx, ev.AggregateID)
		if err != nil {
			return err
		}
		if o.Status != "COMPLETED" {
			return errors.New("reward source order is not completed")
		}
		relation, err := loadRewardRelation(ctx, tx, o.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Serialize rule/cap decisions for this inviter without locking account
		// rows in the opposite order to order settlement or device management.
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "invite-rewards:"+strconv.FormatInt(relation.Inviter, 10)); err != nil {
			return err
		}
		at := now
		if o.CompletedAt != nil {
			at = *o.CompletedAt
		}
		rows, err := tx.Query(ctx, `SELECT id,name,trigger_event,beneficiary,reward_type,reward_value,reward_percent_bp,min_order_amount_micro,freeze_hours,daily_cap_per_inviter,version,starts_at,ends_at FROM invite_reward_rules WHERE status='ENABLED' AND trigger_event IN ('INVITEE_FIRST_PAID','INVITEE_PAID') AND created_at <= $1 AND (starts_at IS NULL OR starts_at <= $1) AND (ends_at IS NULL OR ends_at > $1) ORDER BY id FOR SHARE`, at)
		if err != nil {
			return err
		}
		var rules []rewardRule
		for rows.Next() {
			var r rewardRule
			if err = rows.Scan(&r.ID, &r.Name, &r.Trigger, &r.Beneficiary, &r.Kind, &r.Value, &r.Percent, &r.Minimum, &r.Freeze, &r.Cap, &r.Version, &r.Starts, &r.Ends); err != nil {
				rows.Close()
				return err
			}
			rules = append(rules, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.Minimum != nil && *rule.Minimum >= 0 && o.Base < *rule.Minimum {
				continue
			}
			ref := "order:" + strconv.FormatInt(o.ID, 10)
			if rule.Trigger == "INVITEE_FIRST_PAID" {
				// A paid but unfinished earlier order reserves first-payment
				// eligibility. Its completion event will generate the reward;
				// later/out-of-order events cannot claim a second first payment.
				var first int64
				err = tx.QueryRow(ctx, `SELECT id FROM orders WHERE user_id=$1 AND (paid_at IS NOT NULL OR status IN ('PAID','COMPLETED')) ORDER BY paid_at ASC NULLS FIRST,id LIMIT 1`, o.UserID).Scan(&first)
				if err != nil {
					return err
				}
				if first != o.ID {
					continue
				}
				ref = "first_paid"
			}
			beneficiary := relation.Inviter
			if rule.Beneficiary == "INVITEE" {
				beneficiary = relation.Invitee
			}
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invite_rewards WHERE rule_id=$1 AND beneficiary_user_id=$2 AND invitee_user_id=$3 AND trigger_ref=$4)`, rule.ID, beneficiary, relation.Invitee, ref).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			value, valid := calculateReward(rule, o.Base)
			status, flags, err := rewardRisk(ctx, tx, o, relation, now)
			if err != nil {
				return err
			}
			if !valid {
				status, flags = reviewReward(status, flags, "INVALID_RULE")
			}
			if value == 0 && valid && status != "CANCELLED" {
				status = "CANCELLED"
				flags = append(flags, "ZERO_REWARD")
			}
			if rule.Trigger == "INVITEE_PAID" && (rule.Cap == nil || *rule.Cap <= 0) {
				status, flags = reviewReward(status, flags, "DAILY_CAP_REQUIRED")
			}
			if rule.Cap != nil {
				var count int
				if err = tx.QueryRow(ctx, `SELECT count(*) FROM invite_rewards WHERE inviter_user_id=$1 AND rule_id=$2 AND created_at>$3 AND status<>'CANCELLED'`, relation.Inviter, rule.ID, now.Add(-24*time.Hour)).Scan(&count); err != nil {
					return err
				}
				if *rule.Cap <= 0 || count >= *rule.Cap {
					status, flags = reviewReward(status, flags, "DAILY_CAP_EXCEEDED")
				}
			}
			snapshot := rewardSnapshot{Version: 1, Rule: rule, OrderID: o.ID, OrderBase: o.Base, OrderPay: o.Pay, PaidAt: o.PaidAt, CompletedAt: o.CompletedAt, Value: value}
			if rule.Kind == "DAYS" || rule.Kind == "TRAFFIC" {
				p, err := loadRewardPlan(ctx, tx, rule.Kind)
				if err != nil {
					return err
				}
				snapshot.GrantPlan = p
				if p == nil {
					var active bool
					if rule.Kind == "DAYS" {
						if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscriptions WHERE user_id=$1 AND tier>0 AND expire_at>$2)`, beneficiary, now).Scan(&active); err != nil {
							return err
						}
					}
					if !active {
						status, flags = reviewReward(status, flags, "REWARD_PLAN_REQUIRED")
					}
				}
			}
			release := now
			if rule.Freeze >= 0 && rule.Freeze <= 8760 {
				release = now.Add(time.Duration(rule.Freeze) * time.Hour)
			} else {
				status, flags = reviewReward(status, flags, "INVALID_FREEZE")
			}
			snapRaw, err := json.Marshal(snapshot)
			if err != nil {
				return err
			}
			flagRaw, _ := json.Marshal(flags)
			_, err = tx.Exec(ctx, `INSERT INTO invite_rewards(rule_id,rule_snapshot,inviter_user_id,invitee_user_id,beneficiary_user_id,trigger_ref,reward_type,reward_value,status,risk_flags,release_at,cancel_reason,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(rule_id,beneficiary_user_id,invitee_user_id,trigger_ref) DO NOTHING`, rule.ID, snapRaw, relation.Inviter, relation.Invitee, beneficiary, ref, rule.Kind, value, status, flagRaw, release, rewardReason(status, flags), now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// RuleShape 是一条奖励规则里**与具体订单无关**的那部分字段。
//
// 导出它是为了让后台改规则时能用与发放侧完全相同的判断：形状不合格的规则不会被静默跳过，
// 而是让**每一笔**奖励都带着 INVALID_RULE / DAILY_CAP_REQUIRED 落进 REVIEW 等人工——
// 也就是把第 18 节刚解开的那个队列重新灌满。这种代价应该在改规则的时候就挡住。
type RuleShape struct {
	Trigger     string
	Beneficiary string
	Kind        string
	Value       int64
	Percent     *int
	Minimum     *int64
	Cap         *int
	Version     int
}

// RuleShapeDefect 返回规则形状上的缺陷代号；返回空串表示形状没问题。
// 代号与发放时打进 risk_flags 的那两个保持一致，便于对照。
func RuleShapeDefect(r RuleShape) string {
	if !ruleShapeValid(r) {
		return "INVALID_RULE"
	}
	// 发放侧：INVITEE_PAID 规则没有每日上限时，每一笔都会被打成 REVIEW。
	if r.Trigger == "INVITEE_PAID" && (r.Cap == nil || *r.Cap <= 0) {
		return "DAILY_CAP_REQUIRED"
	}
	return ""
}

// ruleShapeValid 是 calculateReward 里与订单金额无关的那几条判断，抽出来供两处共用。
func ruleShapeValid(r RuleShape) bool {
	if r.Value < 0 || r.Version <= 0 ||
		(r.Beneficiary != "INVITER" && r.Beneficiary != "INVITEE") ||
		(r.Minimum != nil && *r.Minimum < 0) {
		return false
	}
	if r.Percent != nil {
		return r.Kind == "BALANCE" && *r.Percent >= 0 && *r.Percent <= 10000 && r.Value == 0
	}
	switch r.Kind {
	case "DAYS":
		return r.Value <= 36500
	case "TRAFFIC":
		return r.Value <= 1<<53-1
	case "BALANCE":
		return true
	}
	return false
}

func (r rewardRule) shape() RuleShape {
	return RuleShape{Trigger: r.Trigger, Beneficiary: r.Beneficiary, Kind: r.Kind, Value: r.Value,
		Percent: r.Percent, Minimum: r.Minimum, Cap: r.Cap, Version: r.Version}
}

func calculateReward(rule rewardRule, base int64) (int64, bool) {
	if base <= 0 || !ruleShapeValid(rule.shape()) {
		return 0, false
	}
	v := rule.Value
	if rule.Percent != nil {
		n := new(big.Int).Mul(big.NewInt(base), big.NewInt(int64(*rule.Percent)))
		n.Quo(n, big.NewInt(10000))
		if !n.IsInt64() {
			return 0, false
		}
		v = n.Int64()
	}
	// 形状已在上面统一校验过；这里只剩按金额算出来的那个值本身的上限。
	if rule.Kind == "DAYS" {
		return v, v <= 36500
	}
	if rule.Kind == "TRAFFIC" {
		return v, v <= 1<<53-1
	}
	return v, rule.Kind == "BALANCE"
}

// RewardPlanConfigKey 返回某类奖励取套餐编号用的配置键，以及该编号必须对应的套餐类型。
//
// 导出它是为了让后台改配置时能用**与这里完全相同的一套判断**去校验：写进去一个不存在、
// 已下架或类型不符的编号，奖励不会报错，只会在发放时静默跳过（下面那几处 return nil, nil）。
func RewardPlanConfigKey(kind string) (key, planType string) {
	if kind == "TRAFFIC" {
		return "invite.reward_traffic_plan_code", "TRAFFIC"
	}
	return "invite.reward_default_plan_code", "TIME"
}

// LookupRewardPlan 按编号取奖励套餐并校验它是否真的可用于该类奖励。
// 不可用时返回 (nil, nil)——与配置缺失同样处理：不发奖励，但不报错。
func LookupRewardPlan(ctx context.Context, tx pgx.Tx, kind, code string) (*entitlement.Plan, error) {
	if code == "" {
		return nil, nil
	}
	_, typ := RewardPlanConfigKey(kind)
	var p entitlement.Plan
	err := tx.QueryRow(ctx, `SELECT id,code,name,plan_type,tier,COALESCE(duration_days,0),period_quota_bytes,COALESCE(pack_bytes,0),COALESCE(validity_days,0),COALESCE(device_limit,0),price_micro,version FROM plans WHERE code=$1 AND status='ON_SALE' AND plan_type=$2`, code, typ).Scan(&p.ID, &p.Code, &p.Name, &p.Type, &p.Tier, &p.DurationDays, &p.PeriodQuotaBytes, &p.PackBytes, &p.ValidityDays, &p.DeviceLimit, &p.PriceMicro, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validRewardPlan(&p, kind) {
		return nil, nil
	}
	return &p, nil
}

func loadRewardPlan(ctx context.Context, tx pgx.Tx, kind string) (*entitlement.Plan, error) {
	key, _ := RewardPlanConfigKey(kind)
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM system_configs WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var code string
	if json.Unmarshal(raw, &code) != nil || code == "" {
		return nil, nil
	}
	return LookupRewardPlan(ctx, tx, kind, code)
}

func validRewardPlan(p *entitlement.Plan, kind string) bool {
	if p == nil || p.ID <= 0 || p.Tier < 1 || p.Tier > 9 || p.DeviceLimit <= 0 || p.Version <= 0 {
		return false
	}
	if kind == "DAYS" {
		return p.Type == "TIME" && (p.PeriodQuotaBytes == nil || *p.PeriodQuotaBytes > 0)
	}
	return kind == "TRAFFIC" && p.Type == "TRAFFIC" && p.ValidityDays > 0 && p.ValidityDays <= 36500
}
func reviewReward(status string, flags []string, reason string) (string, []string) {
	if status != "CANCELLED" {
		status = "REVIEW"
	}
	return status, append(flags, reason)
}
func rewardReason(status string, flags []string) any {
	if status == "CANCELLED" && len(flags) > 0 {
		return flags[len(flags)-1]
	}
	return nil
}

// ReleaseDue releases at most 50 rewards per tick. Each grant and RELEASED
// transition commits atomically; a failure leaves PENDING for a later retry.
func (s *RewardService) ReleaseDue(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	now = now.UTC()
	for i := 0; i < 50; i++ {
		found := false
		err := rewardTransaction(ctx, s.Pool, func(tx pgx.Tx) error {
			var r pendingReward
			err := tx.QueryRow(ctx, `SELECT id,rule_id,inviter_user_id,invitee_user_id,beneficiary_user_id,reward_type,reward_value,trigger_ref,rule_snapshot FROM invite_rewards WHERE status='PENDING' AND release_at<=$1 ORDER BY release_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, now).Scan(&r.ID, &r.RuleID, &r.Inviter, &r.Invitee, &r.Beneficiary, &r.Kind, &r.Value, &r.Ref, &r.Snapshot)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			return s.releaseReward(ctx, tx, r, now)
		})
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
	return nil
}

func (s *RewardService) releaseReward(ctx context.Context, tx pgx.Tx, r pendingReward, now time.Time) error {
	var snap rewardSnapshot
	if json.Unmarshal(r.Snapshot, &snap) != nil || snap.Version != 1 || snap.Rule.ID != r.RuleID || snap.Rule.Kind != r.Kind || snap.Value != r.Value || snap.OrderID <= 0 {
		return changeRewardStatus(ctx, tx, r.ID, "REVIEW", []string{"INVALID_SNAPSHOT"}, now)
	}
	expectBeneficiary := r.Inviter
	if snap.Rule.Beneficiary == "INVITEE" {
		expectBeneficiary = r.Invitee
	}
	value, valid := calculateReward(snap.Rule, snap.OrderBase)
	if !valid || value != r.Value || r.Value <= 0 || expectBeneficiary != r.Beneficiary {
		return changeRewardStatus(ctx, tx, r.ID, "REVIEW", []string{"INVALID_SNAPSHOT"}, now)
	}
	o, err := loadRewardOrder(ctx, tx, snap.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return changeRewardStatus(ctx, tx, r.ID, "CANCELLED", []string{"SOURCE_ORDER_MISSING"}, now)
	}
	if err != nil {
		return err
	}
	if o.UserID != r.Invitee || o.Base != snap.OrderBase || o.Pay != snap.OrderPay || !sameRewardTime(o.PaidAt, snap.PaidAt) || !sameRewardTime(o.CompletedAt, snap.CompletedAt) {
		return changeRewardStatus(ctx, tx, r.ID, "CANCELLED", []string{"SOURCE_ORDER_CHANGED"}, now)
	}
	relation, err := loadRewardRelation(ctx, tx, r.Invitee)
	if errors.Is(err, pgx.ErrNoRows) {
		return changeRewardStatus(ctx, tx, r.ID, "CANCELLED", []string{"RELATION_MISSING"}, now)
	}
	if err != nil {
		return err
	}
	if relation.Inviter != r.Inviter {
		return changeRewardStatus(ctx, tx, r.ID, "CANCELLED", []string{"RELATION_CHANGED"}, now)
	}
	status, flags, err := rewardRisk(ctx, tx, o, relation, now)
	if err != nil {
		return err
	}
	if snap.Rule.Trigger == "INVITEE_FIRST_PAID" {
		var first int64
		if err = tx.QueryRow(ctx, `SELECT id FROM orders WHERE user_id=$1 AND (paid_at IS NOT NULL OR status IN ('PAID','COMPLETED')) ORDER BY paid_at ASC NULLS FIRST,id LIMIT 1`, r.Invitee).Scan(&first); err != nil {
			return err
		}
		if first != o.ID || r.Ref != "first_paid" {
			status, flags = reviewReward(status, flags, "FIRST_PAYMENT_CHANGED")
		}
	} else if snap.Rule.Trigger != "INVITEE_PAID" || r.Ref != "order:"+strconv.FormatInt(o.ID, 10) {
		status, flags = reviewReward(status, flags, "INVALID_TRIGGER")
	}
	if status != "PENDING" {
		return changeRewardStatus(ctx, tx, r.ID, status, flags, now)
	}
	key := "invite_reward:" + strconv.FormatInt(r.ID, 10)
	var prior bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM balance_ledger WHERE idempotency_key=$1) OR EXISTS(SELECT 1 FROM subscription_ledger WHERE idempotency_key=$1)`, key).Scan(&prior); err != nil {
		return err
	}
	if prior {
		return changeRewardStatus(ctx, tx, r.ID, "REVIEW", []string{"PREEXISTING_LEDGER"}, now)
	}
	review, err := grantReward(ctx, tx, r, snap, key, now)
	if err != nil {
		return err
	}
	if review != "" {
		return changeRewardStatus(ctx, tx, r.ID, "REVIEW", []string{review}, now)
	}
	return changeRewardStatus(ctx, tx, r.ID, "RELEASED", []string{}, now)
}
func sameRewardTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
func changeRewardStatus(ctx context.Context, tx pgx.Tx, id int64, status string, flags []string, now time.Time) error {
	raw, _ := json.Marshal(flags)
	tag, err := tx.Exec(ctx, `UPDATE invite_rewards SET status=$2,risk_flags=risk_flags||$3::jsonb,cancel_reason=$4,released_at=CASE WHEN $2='RELEASED' THEN $5 ELSE released_at END WHERE id=$1 AND status='PENDING'`, id, status, raw, rewardReason(status, flags), now)
	if err == nil && tag.RowsAffected() != 1 {
		return fmt.Errorf("reward %d changed while locked", id)
	}
	return err
}
