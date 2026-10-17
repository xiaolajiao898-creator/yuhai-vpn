package invite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

// DB accepts a pool in production and a rollback-only transaction in tests.
// This service deliberately performs no relationship or accounting writes.
type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Service struct {
	Pool           DB
	Clock          clock.Clock
	InviteBaseURL  string // Optional trusted HTTPS website origin; never derived from request headers.
	BindingEnabled bool   // Enabled only where the authenticated bind handler is configured.
	RewardsEnabled bool   // Runtime gate; activity also requires an effective rule.
}

type Stats struct {
	Invited         int64 `json:"invited"`
	Paid            int64 `json:"paid"`
	RewardsPending  int64 `json:"rewards_pending"`
	RewardsReleased int64 `json:"rewards_released"`
}

type Info struct {
	InviteCode     string     `json:"invite_code"`
	InviteURL      *string    `json:"invite_url"`
	Stats          Stats      `json:"stats"`
	CanBind        bool       `json:"can_bind"`
	BindingEnabled bool       `json:"binding_enabled"`
	RewardsEnabled bool       `json:"rewards_enabled"`
	BindStatus     string     `json:"bind_status"`
	BindDeadline   *time.Time `json:"bind_deadline"`
	Rules          string     `json:"rules"`
}

type Reward struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	Kind      string     `json:"kind"`
	Days      *int64     `json:"days,omitempty"`
	Bytes     *int64     `json:"bytes,omitempty"`
	Amount    *string    `json:"amount,omitempty"`
	Status    string     `json:"status"`
	ReleaseAt *time.Time `json:"release_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type RewardPage struct {
	Items      []Reward `json:"items"`
	NextCursor *string  `json:"next_cursor"`
}

func (s *Service) Summary(ctx context.Context, userID int64) (Info, error) {
	var info Info
	if userID <= 0 {
		return info, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var createdAt time.Time
	var status string
	var bound, hasPaid, hasRewardRule bool
	// One statement gives all counters the same database snapshot. A paid invitee
	// counts once even when they completed several orders. Reward stats count rows,
	// never combine days, bytes and money into a misleading earned total.
	err := s.Pool.QueryRow(ctx, `SELECT u.invite_code, u.created_at, u.status,
	 EXISTS(SELECT 1 FROM invite_relations r WHERE r.invitee_user_id=u.id),
	 EXISTS(SELECT 1 FROM orders o WHERE o.user_id=u.id
	   AND (o.paid_at IS NOT NULL OR o.status IN ('PAID','COMPLETED'))),
	 (SELECT count(*) FROM invite_relations r WHERE r.inviter_user_id=u.id),
	 (SELECT count(*) FROM invite_relations r WHERE r.inviter_user_id=u.id
	   AND EXISTS(SELECT 1 FROM orders o WHERE o.user_id=r.invitee_user_id
	     AND o.status='COMPLETED')),
	 (SELECT count(*) FROM invite_rewards w WHERE w.beneficiary_user_id=u.id
	   AND w.status IN ('PENDING','REVIEW')),
	 (SELECT count(*) FROM invite_rewards w WHERE w.beneficiary_user_id=u.id
	   AND w.status='RELEASED'),
	 ($2::boolean AND EXISTS(SELECT 1 FROM invite_reward_rules r
	   WHERE r.status='ENABLED' AND r.trigger_event IN ('INVITEE_FIRST_PAID','INVITEE_PAID')
	     AND r.created_at<=$3 AND (r.starts_at IS NULL OR r.starts_at<=$3)
	     AND (r.ends_at IS NULL OR r.ends_at>$3)))
	 FROM users u WHERE u.id=$1`, userID, s.RewardsEnabled, s.Clock.Now()).Scan(&info.InviteCode, &createdAt, &status,
		&bound, &hasPaid, &info.Stats.Invited, &info.Stats.Paid,
		&info.Stats.RewardsPending, &info.Stats.RewardsReleased, &hasRewardRule)
	if errors.Is(err, pgx.ErrNoRows) {
		return info, httpx.NewError(httpx.ErrTokenMissing, "账户身份无效")
	}
	if err != nil {
		return info, err
	}
	deadline := createdAt.Add(72 * time.Hour)
	info.BindDeadline = &deadline
	now := s.Clock.Now()
	info.BindingEnabled = s.BindingEnabled
	info.RewardsEnabled = hasRewardRule
	switch {
	case !s.BindingEnabled:
		info.BindStatus = "DISABLED"
	case status != "ACTIVE":
		info.BindStatus = "BLOCKED"
	case bound:
		info.BindStatus = "BOUND"
	case hasPaid:
		info.BindStatus = "PAID"
	case now.Before(createdAt) || !now.Before(deadline):
		info.BindStatus = "EXPIRED"
	default:
		info.BindStatus = "AVAILABLE"
	}
	info.CanBind = info.BindStatus == "AVAILABLE"
	info.InviteURL = inviteURL(s.InviteBaseURL, info.InviteCode)
	info.Rules = "注册后 72 小时内且首次付费前可绑定邀请人，绑定后不可修改。奖励以实际启用的活动规则和发放流水为准，绑定成功不代表已获得奖励。待处理和已发放数字均为奖励记录笔数，不代表收益金额；待处理包含冻结中与审核中的记录。"
	return info, nil
}

func (s *Service) Rewards(ctx context.Context, userID int64, limit int, before int64) (RewardPage, error) {
	page := RewardPage{Items: make([]Reward, 0)}
	if userID <= 0 {
		return page, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份")
	}
	if limit < 1 || limit > 100 || before < 0 {
		return page, badPagination()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT id,
	 CASE WHEN beneficiary_user_id=inviter_user_id THEN 'INVITER' ELSE 'INVITEE' END,
	 reward_type, reward_value, status, release_at, created_at
	 FROM invite_rewards WHERE beneficiary_user_id=$1 AND ($2::bigint=0 OR id<$2)
	 ORDER BY id DESC LIMIT $3`, userID, before, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var reward Reward
		var id, value int64
		if err := rows.Scan(&id, &reward.Role, &reward.Kind, &value, &reward.Status, &reward.ReleaseAt, &reward.CreatedAt); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			cursor := rewardCursor(userID, page.Items[len(page.Items)-1].ID)
			page.NextCursor = &cursor
			break
		}
		reward.ID = strconv.FormatInt(id, 10)
		if value < 0 {
			return page, fmt.Errorf("invite reward %d has a negative value", id)
		}
		switch reward.Kind {
		case "DAYS":
			reward.Days = &value
		case "TRAFFIC":
			reward.Bytes = &value
		case "BALANCE":
			amount := money.Micro(value).String()
			reward.Amount = &amount
		default:
			return page, fmt.Errorf("invite reward %d has an unsupported type", id)
		}
		switch reward.Status {
		case "CANCELLED":
			reward.Status = "REJECTED" // Database lifecycle -> existing public API contract.
			reward.ReleaseAt = nil
		case "REVIEW":
			reward.ReleaseAt = nil // Review has no guaranteed automatic release time.
		case "PENDING", "RELEASED":
		default:
			return page, fmt.Errorf("invite reward %d has an unsupported status", id)
		}
		page.Items = append(page.Items, reward)
	}
	return page, rows.Err()
}

func inviteURL(base, code string) *string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil
	}
	// Generated codes are six characters. Invalid stored data must not become a
	// path injection or an apparently valid share link.
	if len(code) != 6 || strings.Trim(code, "23456789ABCDEFGHJKMNPQRSTUVWXYZ") != "" {
		return nil
	}
	u.Path = "/i/" + code
	u.RawPath = ""
	result := u.String()
	return &result
}
