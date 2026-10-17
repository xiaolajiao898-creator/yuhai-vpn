// Package plan 实现套餐列表、报价与折算（方案第 5 章、11.4）。
// 报价结果写入 plan_quotes，10 分钟有效；下单只引用 quote_id，客户端提交的金额一律不被信任。
package plan

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

const QuoteTTL = 10 * time.Minute

type Service struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

// ---------- 输出结构 ----------

type PlanView struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Tier         int     `json:"tier"`
	PlanVersion  int     `json:"plan_version"`
	ListPrice    string  `json:"list_price"`
	DurationDays *int    `json:"duration_days"`
	QuotaBytes   *int64  `json:"quota_bytes"`
	PeriodDays   *int    `json:"period_days"`
	DeviceLimit  *int    `json:"device_limit"`
	Badge        *string `json:"badge"`
}

type Line struct {
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

type Quote struct {
	QuoteID          string     `json:"quote_id"`
	Plan             PlanView   `json:"plan"`
	ChangeType       string     `json:"change_type"` // NEW / RENEW / TIER_CHANGE / ADDON / TRAFFIC_PACK
	ListPrice        string     `json:"list_price"`
	Credit           string     `json:"credit"`
	BaseAmount       string     `json:"base_amount"`
	Lines            []Line     `json:"lines"`
	NewExpireAt      *time.Time `json:"new_expire_at"`
	BalanceAvailable string     `json:"balance_available"`
	ExpiresAt        time.Time  `json:"expires_at"`
}

// quoteDetail 持久化到 plan_quotes.detail，下单时随订单快照保存。
type quoteDetail struct {
	ChangeType  string           `json:"change_type"`
	ListPrice   int64            `json:"list_price_micro"`
	Credit      int64            `json:"credit_micro"`
	Lines       []Line           `json:"lines"`
	NewExpireAt *time.Time       `json:"new_expire_at"`
	Plan        entitlement.Plan `json:"plan"`
}

// ---------- 套餐列表 ----------

func (s *Service) List(ctx context.Context) ([]PlanView, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, code, name, plan_type, tier, duration_days, period_quota_bytes, pack_bytes, validity_days,
		       device_limit, extra_devices, price_micro, display_config, version
		  FROM plans WHERE status = 'ON_SALE' ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlanView{}
	for rows.Next() {
		p, display, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, toView(p, display))
	}
	return out, rows.Err()
}

func toView(p entitlement.Plan, display map[string]any) PlanView {
	v := PlanView{Code: p.Code, Name: p.Name, Type: p.Type, Tier: p.Tier, PlanVersion: p.Version, ListPrice: money.Micro(p.PriceMicro).String()}
	if b, ok := display["badge"].(string); ok && b != "" {
		v.Badge = &b
	}
	switch p.Type {
	case "TIME":
		d, pd := p.DurationDays, 30
		v.DurationDays, v.QuotaBytes, v.DeviceLimit = &d, p.PeriodQuotaBytes, &p.DeviceLimit
		if p.PeriodQuotaBytes != nil {
			v.PeriodDays = &pd
		}
	case "TRAFFIC":
		d, q := p.ValidityDays, p.PackBytes
		v.DurationDays, v.QuotaBytes, v.DeviceLimit = &d, &q, &p.DeviceLimit
	case "DEVICE_ADDON":
		v.DeviceLimit = &p.ExtraDevices
	}
	return v
}

type scanner interface{ Scan(dest ...any) error }

func scanPlan(r scanner) (entitlement.Plan, map[string]any, error) {
	var p entitlement.Plan
	var dur, val, dev, extra *int
	var pack *int64
	var display []byte
	err := r.Scan(&p.ID, &p.Code, &p.Name, &p.Type, &p.Tier, &dur, &p.PeriodQuotaBytes, &pack, &val, &dev, &extra, &p.PriceMicro, &display, &p.Version)
	if err != nil {
		return p, nil, err
	}
	deref := func(i *int) int {
		if i == nil {
			return 0
		}
		return *i
	}
	p.DurationDays, p.ValidityDays, p.DeviceLimit, p.ExtraDevices = deref(dur), deref(val), deref(dev), deref(extra)
	if pack != nil {
		p.PackBytes = *pack
	}
	m := map[string]any{}
	_ = json.Unmarshal(display, &m)
	return p, m, nil
}

func (s *Service) planByCode(ctx context.Context, code string) (entitlement.Plan, map[string]any, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, code, name, plan_type, tier, duration_days, period_quota_bytes, pack_bytes, validity_days,
		       device_limit, extra_devices, price_micro, display_config, version
		  FROM plans WHERE code = $1 AND status = 'ON_SALE'`, code)
	return scanPlan(row)
}

// ---------- 报价 ----------

func (s *Service) Quote(ctx context.Context, userID int64, planCode string) (Quote, error) {
	now := s.Clock.Now()
	p, display, err := s.planByCode(ctx, planCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return Quote{}, httpx.NewError(httpx.ErrPlanChanged, "套餐已下架或已改价")
	} else if err != nil {
		return Quote{}, err
	}
	sub, curPrice, curSeconds, err := s.currentSubscription(ctx, userID)
	if err != nil {
		return Quote{}, err
	}
	var bal int64
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT available_micro FROM user_balances WHERE user_id = $1), 0)`, userID).Scan(&bal); err != nil {
		return Quote{}, err
	}

	d := quoteDetail{ListPrice: p.PriceMicro, Plan: p}
	list := money.Micro(p.PriceMicro)
	var base money.Micro
	switch p.Type {
	case "TIME":
		dur := time.Duration(p.DurationDays) * 24 * time.Hour
		switch {
		case sub.Valid(now) && sub.Tier == p.Tier:
			d.ChangeType = "RENEW"
			ne := sub.ExpireAt.Add(dur)
			d.NewExpireAt = &ne
			base = list
			d.Lines = []Line{{p.Name, list.String()}}
		case sub.Valid(now):
			// 跨等级：剩余价值折算为抵扣金额（方案 11.4 示例）；上限为新套餐价格
			d.ChangeType = "TIER_CHANGE"
			remaining := int64(sub.ExpireAt.Sub(now).Seconds())
			credit := money.Micro(0)
			if curSeconds > 0 {
				credit = money.Micro(remaining * curPrice / curSeconds)
			}
			if credit > list {
				credit = list
			}
			raw := list - credit
			base = money.CeilToTenth(raw)
			if base == 0 {
				base = money.TenthUSDT
			}
			d.Credit = int64(credit)
			ne := now.Add(dur)
			d.NewExpireAt = &ne
			d.Lines = []Line{
				{p.Name, list.String()},
				{"当前套餐剩余 " + strconv.Itoa(int(remaining/86400)) + " 天折算", (-credit).String()},
			}
			if base != raw {
				d.Lines = append(d.Lines, Line{"向上取整到 0.1 USDT", (base - raw).String()})
			}
		default:
			d.ChangeType = "NEW"
			ne := now.Add(dur)
			d.NewExpireAt = &ne
			base = list
			d.Lines = []Line{{p.Name, list.String()}}
		}
	case "TRAFFIC":
		d.ChangeType = "TRAFFIC_PACK"
		base = list
		d.Lines = []Line{{p.Name, list.String()}}
	case "DEVICE_ADDON":
		if !sub.Valid(now) {
			return Quote{}, httpx.NewError(httpx.ErrNoPlan, "设备加购需要有效的时长套餐")
		}
		d.ChangeType = "ADDON"
		remainingDays := int64(sub.ExpireAt.Sub(now).Hours()/24) + 1 // 不足一天按一天
		raw := money.Micro(int64(list) * remainingDays / 30)
		base = money.CeilToTenth(raw)
		if base == 0 {
			base = money.TenthUSDT
		}
		d.NewExpireAt = sub.ExpireAt
		d.Lines = []Line{{p.Name + "（每 30 天）", list.String()}, {"按剩余 " + strconv.FormatInt(remainingDays, 10) + " 天计价", (raw - list).String()}}
		if base != raw {
			d.Lines = append(d.Lines, Line{"向上取整到 0.1 USDT", (base - raw).String()})
		}
	}

	detail, _ := json.Marshal(d)
	expires := now.Add(QuoteTTL)
	var id string
	if err := s.Pool.QueryRow(ctx, `
		INSERT INTO plan_quotes (user_id, plan_id, plan_version, base_amount_micro, detail, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, userID, p.ID, p.Version, int64(base), detail, expires).Scan(&id); err != nil {
		return Quote{}, err
	}
	return Quote{
		QuoteID: id, Plan: toView(p, display), ChangeType: d.ChangeType, ListPrice: list.String(),
		Credit: money.Micro(d.Credit).String(), BaseAmount: base.String(), Lines: d.Lines, NewExpireAt: d.NewExpireAt,
		BalanceAvailable: money.Micro(bal).String(), ExpiresAt: expires,
	}, nil
}

func (s *Service) currentSubscription(ctx context.Context, userID int64) (entitlement.Subscription, int64, int64, error) {
	var sub entitlement.Subscription
	var price, days *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT s.plan_id, s.tier, s.started_at, s.expire_at, s.device_limit, s.period_quota_bytes, s.period_used_bytes, s.period_reset_at,
		       p.price_micro, p.duration_days
		  FROM subscriptions s LEFT JOIN plans p ON p.id = s.plan_id WHERE s.user_id = $1`, userID).
		Scan(&sub.PlanID, &sub.Tier, &sub.StartedAt, &sub.ExpireAt, &sub.DeviceLimit, &sub.PeriodQuotaBytes, &sub.PeriodUsedBytes, &sub.PeriodResetAt, &price, &days)
	if errors.Is(err, pgx.ErrNoRows) {
		return sub, 0, 0, nil
	}
	if err != nil {
		return sub, 0, 0, err
	}
	var curPrice, curSeconds int64
	if price != nil && days != nil {
		curPrice, curSeconds = *price, *days*86400
	}
	return sub, curPrice, curSeconds, nil
}

// ---------- 供订单模块使用 ----------

type StoredQuote struct {
	ID          string
	UserID      int64
	PlanID      int64
	PlanVersion int
	BaseMicro   int64
	Detail      quoteDetail
	ExpiresAt   time.Time
	Used        bool
}

func (q StoredQuote) ChangeType() string        { return q.Detail.ChangeType }
func (q StoredQuote) Plan() entitlement.Plan    { return q.Detail.Plan }
func (q StoredQuote) Snapshot() ([]byte, error) { return json.Marshal(q.Detail) }
func (q StoredQuote) NewExpireAt() *time.Time   { return q.Detail.NewExpireAt }

// LockQuote 事务内锁定报价并校验归属、有效期、是否已用、套餐版本是否变化。
func LockQuote(ctx context.Context, tx pgx.Tx, quoteID string, userID int64, now time.Time) (StoredQuote, error) {
	var q StoredQuote
	var detail []byte
	var used *int64
	var curVersion int
	var status string
	err := tx.QueryRow(ctx, `
		SELECT q.id::text, q.user_id, q.plan_id, q.plan_version, q.base_amount_micro, q.detail, q.expires_at, q.used_by_order_id, p.version, p.status
		  FROM plan_quotes q JOIN plans p ON p.id = q.plan_id WHERE q.id = $1 FOR UPDATE OF q`, quoteID).
		Scan(&q.ID, &q.UserID, &q.PlanID, &q.PlanVersion, &q.BaseMicro, &detail, &q.ExpiresAt, &used, &curVersion, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && q.UserID != userID) {
		return q, httpx.NewError(httpx.ErrQuoteExpired, "报价已过期，请重新报价")
	}
	if err != nil {
		return q, err
	}
	if used != nil || !now.Before(q.ExpiresAt) {
		return q, httpx.NewError(httpx.ErrQuoteExpired, "报价已过期或已被使用，请重新报价")
	}
	if curVersion != q.PlanVersion || status != "ON_SALE" {
		return q, httpx.NewError(httpx.ErrPlanChanged, "套餐已下架或已改价，请重新报价")
	}
	if err := json.Unmarshal(detail, &q.Detail); err != nil {
		return q, err
	}
	return q, nil
}

func MarkQuoteUsed(ctx context.Context, tx pgx.Tx, quoteID string, orderID int64) error {
	_, err := tx.Exec(ctx, `UPDATE plan_quotes SET used_by_order_id = $2 WHERE id = $1`, quoteID, orderID)
	return err
}
