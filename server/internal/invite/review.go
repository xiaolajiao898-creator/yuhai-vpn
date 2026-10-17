package invite

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// 后台复核入口。
//
// 奖励进入 REVIEW 之后，worker 就再也不碰它了——changeRewardStatus 只更新
// status='PENDING' 的行。也就是说 **REVIEW 是一个只有人能解开的死结**，
// 在这个接口出现之前，任何一笔被风控挂起的奖励都会永远挂着。
//
// 复核放行的语义边界，必须说清楚：
//
//   - **不重跑风控。** 复核的意义就是人已经看过那些风险标记并决定放行；
//     再跑一遍只会把它重新打回 REVIEW，变成死循环。
//   - **但完整性检查一条不省。** 快照必须自洽、来源订单与邀请关系必须还在且没变、
//     同一把幂等 key 不能已经记过账。这些不是风险判断，是「这笔钱该不该是这个数」的
//     算术前提，人无权推翻。任何一条不成立就拒绝放行，并把原因原样报给调用方。
//   - **受益人被封禁则拒绝。** 这不是风控标记，是「钱发给一个已经锁死的账户」，
//     没有任何操作意义。停用（SUSPENDED）不拦——那是可恢复状态。
//
// 拒绝（REJECT）只改状态并留下备注，不触碰任何账目。

// ReviewedReward 描述一笔被后台复核处置的奖励，供审计与响应使用。
type ReviewedReward struct {
	ID          int64    `json:"id,string"`
	Inviter     int64    `json:"inviter_user_id,string"`
	Invitee     int64    `json:"invitee_user_id,string"`
	Beneficiary int64    `json:"beneficiary_user_id,string"`
	Kind        string   `json:"reward_type"`
	Value       int64    `json:"reward_value,string"`
	Status      string   `json:"status"`
	RiskFlags   []string `json:"risk_flags"`
}

// ReviewReward 在调用方的事务里处置一笔处于 REVIEW 的奖励。
//
// 第二个返回值是**拒绝原因**：非空表示这笔奖励没有被处置（调用方应当整笔回滚，
// 并把原因告诉运维），此时 error 为 nil。error 非 nil 才是真正的故障。
// 把「拒绝」和「故障」分成两个返回值，是为了让调用方无法把前者当成后者忽略掉。
func ReviewReward(ctx context.Context, tx pgx.Tx, id int64, approve bool, note string,
	now time.Time) (ReviewedReward, string, error) {
	now = now.UTC()
	var r pendingReward
	var out ReviewedReward
	var status string
	var flags []byte
	err := tx.QueryRow(ctx, `SELECT id,rule_id,inviter_user_id,invitee_user_id,beneficiary_user_id,
 reward_type,reward_value,trigger_ref,rule_snapshot,status,risk_flags
 FROM invite_rewards WHERE id=$1 FOR UPDATE`, id).
		Scan(&r.ID, &r.RuleID, &r.Inviter, &r.Invitee, &r.Beneficiary, &r.Kind, &r.Value,
			&r.Ref, &r.Snapshot, &status, &flags)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, "REWARD_NOT_FOUND", nil
	}
	if err != nil {
		return out, "", err
	}
	// 只处置 REVIEW。PENDING 归 worker，RELEASED / CANCELLED 已成定局——
	// 后台不该有把已发放的奖励改回去的按钮，那属于人工账务调整，是另一条链路。
	if status != "REVIEW" {
		return out, "STATUS_NOT_REVIEW:" + status, nil
	}
	out = ReviewedReward{ID: r.ID, Inviter: r.Inviter, Invitee: r.Invitee, Beneficiary: r.Beneficiary,
		Kind: r.Kind, Value: r.Value, RiskFlags: []string{}}
	if json.Unmarshal(flags, &out.RiskFlags) != nil {
		out.RiskFlags = []string{}
	}

	if !approve {
		out.Status = "CANCELLED"
		if err = finishReview(ctx, tx, r.ID, "CANCELLED", "ADMIN_REJECTED", note, now); err != nil {
			return ReviewedReward{}, "", err
		}
		return out, "", nil
	}

	refusal, err := checkReviewRelease(ctx, tx, r, now)
	if err != nil || refusal != "" {
		return ReviewedReward{}, refusal, err
	}
	out.Status = "RELEASED"
	if err = finishReview(ctx, tx, r.ID, "RELEASED", "ADMIN_APPROVED", note, now); err != nil {
		return ReviewedReward{}, "", err
	}
	return out, "", nil
}

// checkReviewRelease 做完整性核验并在通过时真正发放。返回拒绝原因或错误。
func checkReviewRelease(ctx context.Context, tx pgx.Tx, r pendingReward, now time.Time) (string, error) {
	var snap rewardSnapshot
	if json.Unmarshal(r.Snapshot, &snap) != nil || snap.Version != 1 || snap.Rule.ID != r.RuleID ||
		snap.Rule.Kind != r.Kind || snap.Value != r.Value || snap.OrderID <= 0 {
		return "INVALID_SNAPSHOT", nil
	}
	expect := r.Inviter
	if snap.Rule.Beneficiary == "INVITEE" {
		expect = r.Invitee
	}
	value, valid := calculateReward(snap.Rule, snap.OrderBase)
	if !valid || value != r.Value || r.Value <= 0 || expect != r.Beneficiary {
		return "INVALID_SNAPSHOT", nil
	}
	o, err := loadRewardOrder(ctx, tx, snap.OrderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "SOURCE_ORDER_MISSING", nil
	}
	if err != nil {
		return "", err
	}
	if o.UserID != r.Invitee || o.Base != snap.OrderBase || o.Pay != snap.OrderPay ||
		!sameRewardTime(o.PaidAt, snap.PaidAt) || !sameRewardTime(o.CompletedAt, snap.CompletedAt) {
		return "SOURCE_ORDER_CHANGED", nil
	}
	relation, err := loadRewardRelation(ctx, tx, r.Invitee)
	if errors.Is(err, pgx.ErrNoRows) {
		return "RELATION_MISSING", nil
	}
	if err != nil {
		return "", err
	}
	if relation.Inviter != r.Inviter {
		return "RELATION_CHANGED", nil
	}
	var beneficiaryStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR SHARE`, r.Beneficiary).
		Scan(&beneficiaryStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return "BENEFICIARY_MISSING", nil
	}
	if err != nil {
		return "", err
	}
	if beneficiaryStatus == "BANNED" {
		return "BENEFICIARY_BANNED", nil
	}
	// 幂等 key 与 worker 发放用的是同一把：无论谁先发，第二次都会被这里挡下。
	key := "invite_reward:" + strconv.FormatInt(r.ID, 10)
	var prior bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM balance_ledger WHERE idempotency_key=$1)
 OR EXISTS(SELECT 1 FROM subscription_ledger WHERE idempotency_key=$1)`, key).Scan(&prior); err != nil {
		return "", err
	}
	if prior {
		return "PREEXISTING_LEDGER", nil
	}
	// grantReward 返回的非空字符串，在 worker 那边是「打回 REVIEW 的理由」；
	// 在这里它已经是复核之后了，没有更靠后的人可以打回，所以直接拒绝放行。
	reason, err := grantReward(ctx, tx, r, snap, key, now)
	if err != nil {
		return "", err
	}
	return reason, nil
}

func finishReview(ctx context.Context, tx pgx.Tx, id int64, status, flag, note string, now time.Time) error {
	raw, err := json.Marshal([]string{flag})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE invite_rewards SET status=$2,risk_flags=risk_flags||$3::jsonb,
 cancel_reason=CASE WHEN $2='CANCELLED' THEN $4 ELSE cancel_reason END,
 released_at=CASE WHEN $2='RELEASED' THEN $5 ELSE released_at END
 WHERE id=$1 AND status='REVIEW'`, id, status, raw, note, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		// 行已被 FOR UPDATE 锁住，走到这里说明代码本身有问题，不是并发。
		return errors.New("invite reward changed while locked for review")
	}
	return nil
}
