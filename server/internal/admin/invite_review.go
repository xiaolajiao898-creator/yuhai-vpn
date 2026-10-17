package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/invite"
)

// 邀请关系与邀请奖励：只读列表 + 复核处置（方案 11.5）。
//
// 为什么这组接口必须有：邀请奖励一旦被风控判为可疑，就会停在 REVIEW，
// 而后台 worker **只处理 PENDING**——REVIEW 是一个只有人能解开的死结。
// 在此之前，被挂起的奖励既看不见，也没人能放行或驳回，只能永远挂着。
//
// 三个边界，写在这里免得以后误解：
//
//  1. **复核不重跑风控。** 放行的含义就是「人看过这些风险标记，决定认」。
//     重跑只会把它再次打回 REVIEW，变成死循环。完整性检查（快照自洽、来源订单没变、
//     没有重复记账）则一条不省——那是算术前提，不是风险判断。
//  2. **放行会当场记账。** 余额类奖励走 balance_ledger，时长/流量类会改订阅或发流量包。
//     也就是说这个接口让 admin-api 进程实际获得了写账能力，这是它此前没有的。
//     部署时这一点要与资金侧一起评估；最小权限清单在集成测试里逐列列出。
//  3. **只处置 REVIEW。** PENDING 归 worker，RELEASED / CANCELLED 已成定局。
//     把已发放的奖励改回去属于人工账务调整，是另一条（尚未开工的）链路。
type InviteReadService struct{ Auth *Service }

// InviteReviewService 处置邀请奖励复核。
type InviteReviewService struct{ Security *ApprovalService }

const (
	inviteRead         = "invite.read"
	actionRewardReview = "invite.reward_review"
)

type ManagedInviteRelation struct {
	InviteeUserID string          `json:"invitee_user_id"`
	InviterUserID string          `json:"inviter_user_id"`
	InviteCode    string          `json:"invite_code"`
	BindMethod    string          `json:"bind_method"`
	RiskFlags     json.RawMessage `json:"risk_flags"`
	CreatedAt     time.Time       `json:"created_at"`
}

type ManagedInviteRelationPage struct {
	Items      []ManagedInviteRelation `json:"items"`
	Total      string                  `json:"total"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	ObservedAt time.Time               `json:"observed_at"`
}

// ManagedInviteReward 里 reward_value 的单位**随 reward_type 变**，
// 因此一律附带 value_unit；只有 BALANCE 才有 amount（6 位小数的 USDT）。
// 少了这一列，运维会把 10737418240 字节当成 107 亿 USDT。
type ManagedInviteReward struct {
	ID            string          `json:"id"`
	RuleID        string          `json:"rule_id"`
	InviterUserID string          `json:"inviter_user_id"`
	InviteeUserID string          `json:"invitee_user_id"`
	BeneficiaryID string          `json:"beneficiary_user_id"`
	TriggerRef    string          `json:"trigger_ref"`
	RewardType    string          `json:"reward_type"`
	RewardValue   string          `json:"reward_value"`
	ValueUnit     string          `json:"value_unit"`
	Amount        *string         `json:"amount"`
	Status        string          `json:"status"`
	RiskFlags     json.RawMessage `json:"risk_flags"`
	ReleaseAt     time.Time       `json:"release_at"`
	CancelReason  *string         `json:"cancel_reason"`
	ReleasedAt    *time.Time      `json:"released_at"`
	CreatedAt     time.Time       `json:"created_at"`
}

type ManagedInviteRewardPage struct {
	Items      []ManagedInviteReward `json:"items"`
	Total      string                `json:"total"`
	Page       int                   `json:"page"`
	PageSize   int                   `json:"page_size"`
	ObservedAt time.Time             `json:"observed_at"`
}

type InviteFilter struct {
	Page, PageSize int
	Status         string
	InviterUserID  string
	InviteeUserID  string
	BeneficiaryID  string
}

type RewardReviewInput struct {
	OperationID string `json:"operation_id"`
	Decision    string `json:"decision"`
	Note        string `json:"note"`
}

var rewardStatuses = map[string]bool{"PENDING": true, "REVIEW": true, "RELEASED": true, "CANCELLED": true}

var rewardValueUnits = map[string]string{"DAYS": "DAYS", "TRAFFIC": "BYTES", "BALANCE": "USDT_MICRO"}

func (s *InviteReadService) filterID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := positiveApprovalID(raw)
	if err != nil {
		return 0, httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
	}
	return id, nil
}

// Relations 列出邀请关系。
func (s *InviteReadService) Relations(ctx context.Context, p Principal, f InviteFilter) (out ManagedInviteRelationPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	inviter, err := s.filterID(f.InviterUserID)
	if err != nil {
		return out, err
	}
	invitee, err := s.filterID(f.InviteeUserID)
	if err != nil {
		return out, err
	}
	read := &FinanceReadService{Auth: s.Auth}
	err = read.readWith(ctx, p, inviteRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		// 与资金侧同样的坑：与字面量 0 比较会让 PostgreSQL 把参数推断成 int4，
		// 而用户编号远超 int4 范围，因此显式标注 ::bigint。
		const where = ` FROM invite_relations r
 WHERE ($1::bigint=0 OR r.inviter_user_id=$1::bigint)
   AND ($2::bigint=0 OR r.invitee_user_id=$2::bigint)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, inviter, invitee).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT r.invitee_user_id::text,r.inviter_user_id::text,r.invite_code,
 r.bind_method,r.risk_flags,r.created_at`+where+
			` ORDER BY r.created_at DESC,r.invitee_user_id DESC LIMIT $3 OFFSET $4`,
			inviter, invitee, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedInviteRelationPage{Items: []ManagedInviteRelation{},
			Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var v ManagedInviteRelation
			if e = rows.Scan(&v.InviteeUserID, &v.InviterUserID, &v.InviteCode, &v.BindMethod,
				&v.RiskFlags, &v.CreatedAt); e != nil {
				return e
			}
			if _, e = positiveApprovalID(v.InviteeUserID); e != nil {
				return httpx.Internal
			}
			if _, e = positiveApprovalID(v.InviterUserID); e != nil {
				return httpx.Internal
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedInviteRelationPage{}, err
	}
	return out, nil
}

// Rewards 列出邀请奖励。默认全部状态；运维真正要看的是 status=REVIEW 那一批。
func (s *InviteReadService) Rewards(ctx context.Context, p Principal, f InviteFilter) (out ManagedInviteRewardPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Status != "" && !rewardStatuses[f.Status] {
		return out, httpx.NewError(httpx.ErrBadRequest, "奖励状态无效")
	}
	beneficiary, err := s.filterID(f.BeneficiaryID)
	if err != nil {
		return out, err
	}
	read := &FinanceReadService{Auth: s.Auth}
	err = read.readWith(ctx, p, inviteRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` FROM invite_rewards w WHERE ($1=''OR w.status=$1)
 AND ($2::bigint=0 OR w.beneficiary_user_id=$2::bigint)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, f.Status, beneficiary).Scan(&total); e != nil {
			return e
		}
		// rule_snapshot 不返回：它整份包含规则与订单金额快照，体量大，
		// 且复核真正需要的信息已经在 risk_flags 与金额字段里。
		rows, e := tx.Query(c, `SELECT w.id::text,w.rule_id::text,w.inviter_user_id::text,
 w.invitee_user_id::text,w.beneficiary_user_id::text,w.trigger_ref,w.reward_type,w.reward_value,
 w.status,w.risk_flags,w.release_at,w.cancel_reason,w.released_at,w.created_at`+where+
			` ORDER BY w.created_at DESC,w.id DESC LIMIT $3 OFFSET $4`,
			f.Status, beneficiary, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedInviteRewardPage{Items: []ManagedInviteReward{},
			Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var v ManagedInviteReward
			var value int64
			if e = rows.Scan(&v.ID, &v.RuleID, &v.InviterUserID, &v.InviteeUserID, &v.BeneficiaryID,
				&v.TriggerRef, &v.RewardType, &value, &v.Status, &v.RiskFlags, &v.ReleaseAt,
				&v.CancelReason, &v.ReleasedAt, &v.CreatedAt); e != nil {
				return e
			}
			v.RewardValue, v.ValueUnit = strconv.FormatInt(value, 10), rewardValueUnits[v.RewardType]
			if v.RewardType == "BALANCE" {
				rendered := amount(value)
				v.Amount = &rendered
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedInviteRewardPage{}, err
	}
	return out, nil
}

// ---------- 复核（L2 + 二次验证） ----------

// Review 放行或驳回一笔处于 REVIEW 的邀请奖励。
//
// 放行会在同一事务里真正发放；任何完整性检查不通过都返回 40901 并附上原因码，
// 整笔事务回滚——没有「记了一半」的中间态。
func (s *InviteReviewService) Review(ctx context.Context, p ApprovalProof, rewardID int64,
	in RewardReviewInput) (out invite.ReviewedReward, err error) {
	if s == nil {
		return out, httpx.Internal
	}
	if rewardID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "奖励编号无效")
	}
	approve := in.Decision == "APPROVE"
	if !approve && in.Decision != "REJECT" {
		return out, httpx.NewError(httpx.ErrBadRequest, "decision 只接受 APPROVE 或 REJECT")
	}
	raw, err := securedMutation(ctx, s.Security, "admin_invite_mutations", "admin-invite-review",
		p, actionRewardReview, "invite_reward", rewardID, in.OperationID, in.Note,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			reviewed, refusal, e := invite.ReviewReward(c, tx, rewardID, approve, in.Note, now)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			// 「不存在」是 404，其余拒绝都是 409：前者是找错了对象，
			// 后者是对象在、但当前状态或账目前提不允许这么处置——两种情况运维要采取的动作不同。
			if refusal == "REWARD_NOT_FOUND" {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "奖励不存在")
			}
			if refusal != "" {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "无法处置该奖励："+refusal)
			}
			id := strconv.FormatInt(rewardID, 10)
			before, _ := json.Marshal(map[string]string{"target_id": id, "status": "REVIEW"})
			after, _ := json.Marshal(map[string]string{"target_id": id, "status": reviewed.Status,
				"code": in.Decision, "decision_note": in.Note})
			return rewardID, reviewed, before, after, nil
		})
	if err != nil {
		return invite.ReviewedReward{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return invite.ReviewedReward{}, httpx.Internal
	}
	return out, nil
}
