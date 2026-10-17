package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/invite"
)

// 邀请奖励规则修改（admin.yaml PATCH /invite-rules/{id}）。
//
// 与第 22 节的配置写是同一类缺口：`invite_reward_rules` 此前没有任何接口能写。
// 但这条链路的代价更直接——**形状不合格的规则不会被跳过**：
// 发放侧照样建奖励，只是每一笔都带着 INVALID_RULE 或 DAILY_CAP_REQUIRED 落进 REVIEW，
// 把第 18 节刚解开的那个人工队列重新灌满。所以校验必须在改规则的时候做，
// 而且必须用**与发放侧同一份实现**（internal/invite 的 RuleShapeDefect）。
//
// 三件事刻意不做：
//
//  1. **trigger_event 与 beneficiary 不可修改。** invite_rewards 的去重键是
//     (rule_id, beneficiary_user_id, invitee_user_id, trigger_ref)，而 trigger_ref 由
//     trigger_event 决定（INVITEE_FIRST_PAID 恒为 "first_paid"，INVITEE_PAID 是 "order:<id>"）。
//     改任一个都会让**同一个受益人就同一笔订单再拿一次奖励**——去重键变了，历史那条挡不住新的。
//  2. **reward_type 不可修改。** reward_value 的单位随类型变（天 / 字节 / micro），
//     改类型不改值就是把「30 天」变成「30 字节」。要换类型就新建一条规则。
//  3. **不动已经生成的奖励。** 每笔奖励在创建时就存了完整的 rule_snapshot，按快照结算；
//     改规则只影响之后生成的。停用规则也不会撤回已经排队的 PENDING/REVIEW。
type InviteRuleWriteService struct{ Security *ApprovalService }

const actionInviteRuleWrite = "invite.rule_write"

// 冻结小时数上限：一年。再长的冻结等于不发，应该直接停用规则。
const maxRuleFreezeHours = 24 * 366

type InviteRuleUpdateInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	// 指针区分「没提这个字段」与「显式设成 null」，与套餐修改同一套约定。
	Name       *string     `json:"name"`
	Status     *string     `json:"status"`
	Value      *int64      `json:"reward_value"`
	Percent    **int       `json:"reward_percent_bp"`
	Minimum    **int64     `json:"min_order_amount_micro"`
	Freeze     *int        `json:"freeze_hours"`
	Cap        **int       `json:"daily_cap_per_inviter"`
	StartsAt   **time.Time `json:"starts_at"`
	EndsAt     **time.Time `json:"ends_at"`
	ExpectedNo *string     `json:"expected_version"`
}

type ManagedInviteRule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Trigger     string     `json:"trigger_event"`
	Beneficiary string     `json:"beneficiary"`
	RewardType  string     `json:"reward_type"`
	RewardValue string     `json:"reward_value"`
	PercentBP   *int       `json:"reward_percent_bp"`
	MinOrder    *string    `json:"min_order_amount_micro"`
	FreezeHours int        `json:"freeze_hours"`
	DailyCap    *int       `json:"daily_cap_per_inviter"`
	Status      string     `json:"status"`
	StartsAt    *time.Time `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	Version     string     `json:"version"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Update 修改一条邀请奖励规则。
func (s *InviteRuleWriteService) Update(ctx context.Context, p ApprovalProof, ruleID int64,
	in InviteRuleUpdateInput) (out ManagedInviteRule, err error) {
	if s == nil || s.Security == nil {
		return out, httpx.Internal
	}
	if ruleID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "规则编号无效")
	}
	if in.Name != nil && !nodeText(*in.Name, 64, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "规则名称必须是 1–64 个可见字符")
	}
	if in.Status != nil && *in.Status != "ENABLED" && *in.Status != "DISABLED" {
		return out, httpx.NewError(httpx.ErrBadRequest, "状态只能是 ENABLED 或 DISABLED")
	}
	if in.Freeze != nil && (*in.Freeze < 0 || *in.Freeze > maxRuleFreezeHours) {
		return out, httpx.NewError(httpx.ErrBadRequest, "冻结小时数超出允许范围 0–"+
			strconv.Itoa(maxRuleFreezeHours))
	}
	if in.Cap != nil && *in.Cap != nil && **in.Cap <= 0 {
		// 显式写 0 或负数比不写更糟：发放侧会把每一笔都打成 DAILY_CAP_EXCEEDED。
		return out, httpx.NewError(httpx.ErrBadRequest, "每日上限必须为正数，不限量请显式写 null")
	}
	if in.Value == nil && in.Percent == nil && in.Minimum == nil && in.Freeze == nil &&
		in.Cap == nil && in.Name == nil && in.Status == nil && in.StartsAt == nil && in.EndsAt == nil {
		return out, httpx.NewError(httpx.ErrBadRequest, "没有要修改的字段")
	}

	raw, err := securedMutation(ctx, s.Security, "admin_invite_mutations", "admin-invite-write",
		p, actionInviteRuleWrite, "invite_reward_rule", ruleID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			cur, e := loadInviteRule(c, tx, ruleID)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			// 乐观并发：调用方拿到详情后可以带上 expected_version，
			// 中间被别人改过就整笔拒绝而不是覆盖。不带则不检查。
			if in.ExpectedNo != nil && *in.ExpectedNo != cur.Version {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict,
					"规则已被他人修改（当前版本 "+cur.Version+"）")
			}
			next := applyInviteRuleChanges(cur, in)
			if e = validateInviteRule(next); e != nil {
				return 0, nil, nil, nil, e
			}
			// version 只在**实质字段**变化时 +1：奖励快照里记着它，两份内容不同的快照
			// 带同一个版本号，事后对账就分不清谁是谁。改名字或上下架不动它。
			bump := substantiveRuleChange(cur, next)
			version, e := strconv.Atoi(cur.Version)
			if e != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			if bump {
				version++
			}
			var minOrder *int64
			if next.MinOrder != nil {
				v, pe := strconv.ParseInt(*next.MinOrder, 10, 64)
				if pe != nil {
					return 0, nil, nil, nil, httpx.Internal
				}
				minOrder = &v
			}
			value, e := strconv.ParseInt(next.RewardValue, 10, 64)
			if e != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			if _, e = tx.Exec(c, `UPDATE invite_reward_rules SET name=$2,reward_value=$3,
 reward_percent_bp=$4,min_order_amount_micro=$5,freeze_hours=$6,daily_cap_per_inviter=$7,
 status=$8,starts_at=$9,ends_at=$10,version=$11,updated_at=$12 WHERE id=$1`,
				ruleID, next.Name, value, next.PercentBP, minOrder, next.FreezeHours,
				next.DailyCap, next.Status, next.StartsAt, next.EndsAt, version, now); e != nil {
				return 0, nil, nil, nil, e
			}
			next.Version, next.UpdatedAt = strconv.Itoa(version), now
			before, _ := json.Marshal(map[string]any{"target_id": strconv.FormatInt(ruleID, 10),
				"status": cur.Status, "version_code": cur.Version, "name": cur.Name})
			after, _ := json.Marshal(map[string]any{"target_id": strconv.FormatInt(ruleID, 10),
				"status": next.Status, "version_code": next.Version, "name": next.Name,
				"decision_note": in.Reason})
			return ruleID, next, before, after, nil
		})
	if err != nil {
		return ManagedInviteRule{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedInviteRule{}, httpx.Internal
	}
	return out, nil
}

func loadInviteRule(ctx context.Context, tx pgx.Tx, id int64) (ManagedInviteRule, error) {
	var r ManagedInviteRule
	err := tx.QueryRow(ctx, `SELECT id::text,name,trigger_event,beneficiary,reward_type,
 reward_value::text,reward_percent_bp,min_order_amount_micro::text,freeze_hours,
 daily_cap_per_inviter,status,starts_at,ends_at,version::text,updated_at
 FROM invite_reward_rules WHERE id=$1 FOR UPDATE`, id).
		Scan(&r.ID, &r.Name, &r.Trigger, &r.Beneficiary, &r.RewardType, &r.RewardValue,
			&r.PercentBP, &r.MinOrder, &r.FreezeHours, &r.DailyCap, &r.Status, &r.StartsAt,
			&r.EndsAt, &r.Version, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, httpx.NewError(httpx.ErrNotFound, "规则不存在")
	}
	return r, err
}

// applyInviteRuleChanges 把请求里出现过的字段盖到当前值上。
// 没出现的字段原样保留——这是 PATCH，不是整条替换。
func applyInviteRuleChanges(cur ManagedInviteRule, in InviteRuleUpdateInput) ManagedInviteRule {
	next := cur
	if in.Name != nil {
		next.Name = *in.Name
	}
	if in.Status != nil {
		next.Status = *in.Status
	}
	if in.Value != nil {
		next.RewardValue = strconv.FormatInt(*in.Value, 10)
	}
	if in.Percent != nil {
		next.PercentBP = *in.Percent
	}
	if in.Minimum != nil {
		next.MinOrder = nil
		if *in.Minimum != nil {
			v := strconv.FormatInt(**in.Minimum, 10)
			next.MinOrder = &v
		}
	}
	if in.Freeze != nil {
		next.FreezeHours = *in.Freeze
	}
	if in.Cap != nil {
		next.DailyCap = *in.Cap
	}
	if in.StartsAt != nil {
		next.StartsAt = *in.StartsAt
	}
	if in.EndsAt != nil {
		next.EndsAt = *in.EndsAt
	}
	return next
}

// validateInviteRule 用与发放侧同一份实现判断规则形状，再补上两条本地约束。
func validateInviteRule(r ManagedInviteRule) error {
	value, err := strconv.ParseInt(r.RewardValue, 10, 64)
	if err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "奖励数值无效")
	}
	var minimum *int64
	if r.MinOrder != nil {
		v, e := strconv.ParseInt(*r.MinOrder, 10, 64)
		if e != nil {
			return httpx.NewError(httpx.ErrBadRequest, "最低订单金额无效")
		}
		minimum = &v
	}
	version, err := strconv.Atoi(r.Version)
	if err != nil {
		return httpx.Internal
	}
	switch invite.RuleShapeDefect(invite.RuleShape{Trigger: r.Trigger, Beneficiary: r.Beneficiary,
		Kind: r.RewardType, Value: value, Percent: r.PercentBP, Minimum: minimum,
		Cap: r.DailyCap, Version: version}) {
	case "INVALID_RULE":
		// 与发放侧同一份判断：按比例只能配在 BALANCE 上且 reward_value 必须为 0；
		// DAYS 不超过 36500，TRAFFIC 不超过 2^53-1；最低订单金额不得为负。
		return httpx.NewError(httpx.ErrBadRequest,
			"规则形状不合格：按比例奖励只能用于 BALANCE 且固定值必须为 0；"+
				"DAYS 不得超过 36500、TRAFFIC 不得超过 2^53-1；最低订单金额不得为负")
	case "DAILY_CAP_REQUIRED":
		// INVITEE_PAID 每笔订单都会触发，没有每日上限时发放侧会把每一笔都打进 REVIEW。
		return httpx.NewError(httpx.ErrBadRequest,
			"INVITEE_PAID 规则必须设置每日上限，否则每一笔奖励都会落进人工复核")
	}
	// 固定值与比例都为 0 的规则每笔都会被判为 ZERO_REWARD 直接作废，等于开着却不发。
	if r.PercentBP == nil && value == 0 {
		return httpx.NewError(httpx.ErrBadRequest, "奖励数值为 0 的规则不会发出任何奖励")
	}
	if r.StartsAt != nil && r.EndsAt != nil && !r.EndsAt.After(*r.StartsAt) {
		// 发放侧的窗口是 [starts_at, ends_at)，右端开区间：相等就是空窗口。
		return httpx.NewError(httpx.ErrBadRequest, "结束时间必须晚于开始时间")
	}
	return nil
}

// substantiveRuleChange 判断这次修改是否动了会写进奖励快照的那些字段。
// 名称与上下架不算——每改个名字就推高版本号，会让快照里的版本号失去对账价值。
func substantiveRuleChange(a, b ManagedInviteRule) bool {
	same := func(x, y *int) bool {
		return x == nil && y == nil || x != nil && y != nil && *x == *y
	}
	sameStr := func(x, y *string) bool {
		return x == nil && y == nil || x != nil && y != nil && *x == *y
	}
	sameTime := func(x, y *time.Time) bool {
		return x == nil && y == nil || x != nil && y != nil && x.Equal(*y)
	}
	return a.RewardValue != b.RewardValue || !same(a.PercentBP, b.PercentBP) ||
		!sameStr(a.MinOrder, b.MinOrder) || a.FreezeHours != b.FreezeHours ||
		!same(a.DailyCap, b.DailyCap) || !sameTime(a.StartsAt, b.StartsAt) ||
		!sameTime(a.EndsAt, b.EndsAt)
}
