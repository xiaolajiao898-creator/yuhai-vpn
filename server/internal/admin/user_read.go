package admin

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

const userReadPermission = "users.read"
const userLedgerPermission = "users.ledgers.read"

var managedAccountNo = regexp.MustCompile(`^[1-9][0-9]{9}$`)

type UserReadService struct{ Auth *Service }
type ManagedUserSummary struct {
	ID                string    `json:"id"`
	AccountNo         string    `json:"account_no"`
	Status            string    `json:"status"`
	Balance           *string   `json:"balance"`
	Currency          string    `json:"currency"`
	ActiveDeviceCount string    `json:"active_device_count"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}
type ManagedSubscription struct {
	PlanID           *string    `json:"plan_id"`
	Tier             int        `json:"tier"`
	ExpiresAt        *time.Time `json:"expires_at"`
	DeviceLimit      int        `json:"device_limit"`
	PeriodQuotaBytes *string    `json:"period_quota_bytes"`
	PeriodUsedBytes  string     `json:"period_used_bytes"`
	PeriodResetAt    *time.Time `json:"period_reset_at"`
}
type ManagedUserDetail struct {
	ManagedUserSummary
	InviteCode       string               `json:"invite_code"`
	BalanceUpdatedAt *time.Time           `json:"balance_updated_at"`
	Subscription     *ManagedSubscription `json:"subscription"`
}
type ManagedUserList struct {
	Items    []ManagedUserSummary `json:"items"`
	Total    string               `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
}
type ManagedUserFilter struct {
	Page, PageSize    int
	AccountNo, Status string
}
type ManagedBalanceEntry struct {
	ID           string    `json:"id"`
	EntryType    string    `json:"entry_type"`
	Amount       string    `json:"amount"`
	BalanceAfter string    `json:"balance_after"`
	SourceType   string    `json:"source_type"`
	SourceID     string    `json:"source_id"`
	OperatorType string    `json:"operator_type"`
	CreatedAt    time.Time `json:"created_at"`
}
type ManagedEntitlementEntry struct {
	ID           string    `json:"id"`
	ChangeType   string    `json:"change_type"`
	DeltaSeconds string    `json:"delta_seconds"`
	DeltaBytes   string    `json:"delta_bytes"`
	DeltaDevices string    `json:"delta_devices"`
	SourceType   string    `json:"source_type"`
	SourceID     *string   `json:"source_id"`
	OperatorType string    `json:"operator_type"`
	CreatedAt    time.Time `json:"created_at"`
}
type ManagedLedgerPage struct {
	UserID   string  `json:"user_id"`
	Type     string  `json:"type"`
	Currency *string `json:"currency"`
	Items    any     `json:"items"`
	Total    string  `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

func normalizeUserPage(page, size int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || page > 10000 || size < 1 || size > 50 {
		return 0, 0, httpx.NewError(httpx.ErrBadRequest, "分页参数无效")
	}
	return page, size, nil
}
func normalizeManagedUserFilter(f ManagedUserFilter) (ManagedUserFilter, error) {
	var err error
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return f, err
	}
	if f.AccountNo != "" && !managedAccountNo.MatchString(f.AccountNo) || f.Status != "" && f.Status != "ACTIVE" && f.Status != "SUSPENDED" && f.Status != "BANNED" {
		return f, httpx.NewError(httpx.ErrBadRequest, "用户查询参数无效")
	}
	return f, nil
}

// The business snapshot is read-only and repeatable, so balances, counts and
// items cannot come from different commits. Authorization is queried outside
// that snapshot both before and after it: a revoked session/permission or a
// deadline crossed during a slow read must not return the buffered result.
// This does not lock or mutate user, balance, subscription or ledger rows.
func (s *UserReadService) read(ctx context.Context, p Principal, ledger bool, fn func(context.Context, pgx.Tx) error) (err error) {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil {
		return httpx.Internal
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := func() error { return s.authority(c, p, userReadCodes(ledger)) }
	if err = check(); err != nil {
		return err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollbackApproval(tx)
	if err = fn(c, tx); err != nil {
		return err
	}
	if err = tx.Commit(c); err != nil {
		return err
	}
	return check()
}

func userReadCodes(ledger bool) []string {
	if ledger {
		return []string{"users.read", "users.ledgers.read"}
	}
	return []string{"users.read"}
}

// All required capabilities must hold in the same fresh database snapshot,
// rather than observing each permission separately across a concurrent role
// change. codes must be non-empty and each capability must be risk level 0.
func (s *UserReadService) authority(ctx context.Context, p Principal, codes []string) error {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return errSession
	}
	if len(codes) == 0 {
		return httpx.Internal
	}
	var allowed bool
	err = s.Auth.Pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM admin_users u JOIN admin_sessions session ON session.admin_user_id=u.id
 WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3)
 AND session.id=$2 AND session.revoked_at IS NULL AND session.expires_at>$3 AND session.last_active_at>$3-interval '30 minutes'
 AND (SELECT count(DISTINCT perm.code) FROM admin_user_roles ur
        JOIN admin_role_permissions rp ON rp.role_id=ur.role_id
        JOIN admin_permissions perm ON perm.code=rp.permission_code
       WHERE ur.admin_user_id=u.id AND perm.code = ANY($4::text[]) AND perm.risk_level=0)
     = cardinality($4::text[]))`, id, p.SessionID, s.Auth.Clock.Now(), codes).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return httpx.NewError(httpx.ErrForbidden, "当前会话或用户查看权限已失效")
	}
	return nil
}

// readWith 与 read 相同，但要求额外的只读能力，用于回执等需要独立授权的视图。
func (s *UserReadService) readWith(ctx context.Context, p Principal, codes []string, fn func(context.Context, pgx.Tx) error) (err error) {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil {
		return httpx.Internal
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := func() error { return s.authority(c, p, codes) }
	if err = check(); err != nil {
		return err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollbackApproval(tx)
	if err = fn(c, tx); err != nil {
		return err
	}
	if err = tx.Commit(c); err != nil {
		return err
	}
	return check()
}

const managedUserColumns = `u.id::text,u.account_no,u.status,b.available_micro,(SELECT count(*)::text FROM devices d WHERE d.user_id=u.id AND d.status='ACTIVE'),u.created_at,u.updated_at`

func scanManagedUser(row pgx.Row) (u ManagedUserSummary, err error) {
	var balance *int64
	err = row.Scan(&u.ID, &u.AccountNo, &u.Status, &balance, &u.ActiveDeviceCount, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return u, err
	}
	u.Currency = "USDT"
	if balance != nil {
		v := money.Micro(*balance).String()
		u.Balance = &v
	}
	if !managedAccountNo.MatchString(u.AccountNo) || balance != nil && *balance < 0 {
		return ManagedUserSummary{}, httpx.Internal
	}
	return u, nil
}
func (s *UserReadService) List(ctx context.Context, p Principal, f ManagedUserFilter) (out ManagedUserList, err error) {
	f, err = normalizeManagedUserFilter(f)
	if err != nil {
		return out, err
	}
	err = s.read(ctx, p, false, func(c context.Context, tx pgx.Tx) error {
		where := ` WHERE ($1='' OR u.account_no=$1) AND ($2='' OR u.status=$2)`
		var total int64
		if err := tx.QueryRow(c, `SELECT count(*) FROM users u`+where, f.AccountNo, f.Status).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(c, `SELECT `+managedUserColumns+` FROM users u LEFT JOIN user_balances b ON b.user_id=u.id`+where+` ORDER BY u.created_at DESC,u.id DESC LIMIT $3 OFFSET $4`, f.AccountNo, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = ManagedUserList{Items: []ManagedUserSummary{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize}
		for rows.Next() {
			u, err := scanManagedUser(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, u)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedUserList{}, err
	}
	return out, nil
}
func (s *UserReadService) Get(ctx context.Context, p Principal, id int64) (out ManagedUserDetail, err error) {
	if id <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
	}
	err = s.read(ctx, p, false, func(c context.Context, tx pgx.Tx) error {
		u, err := scanManagedUser(tx.QueryRow(c, `SELECT `+managedUserColumns+` FROM users u LEFT JOIN user_balances b ON b.user_id=u.id WHERE u.id=$1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "用户不存在")
		}
		if err != nil {
			return err
		}
		out.ManagedUserSummary = u
		if err = tx.QueryRow(c, `SELECT u.invite_code,b.updated_at FROM users u LEFT JOIN user_balances b ON b.user_id=u.id WHERE u.id=$1`, id).Scan(&out.InviteCode, &out.BalanceUpdatedAt); err != nil {
			return err
		}
		if !utf8.ValidString(out.InviteCode) || out.InviteCode == "" || utf8.RuneCountInString(out.InviteCode) > 12 {
			return httpx.Internal
		}
		for _, r := range out.InviteCode {
			if unicode.IsControl(r) {
				return httpx.Internal
			}
		}
		sub := &ManagedSubscription{}
		var quota *int64
		var used int64
		err = tx.QueryRow(c, `SELECT plan_id::text,tier,expire_at,device_limit,period_quota_bytes,period_used_bytes,period_reset_at FROM subscriptions WHERE user_id=$1`, id).Scan(&sub.PlanID, &sub.Tier, &sub.ExpiresAt, &sub.DeviceLimit, &quota, &used, &sub.PeriodResetAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if sub.Tier < 0 || sub.Tier > 9 || sub.DeviceLimit < 0 || used < 0 || quota != nil && *quota < 0 {
			return httpx.Internal
		}
		sub.PeriodUsedBytes = strconv.FormatInt(used, 10)
		if quota != nil {
			v := strconv.FormatInt(*quota, 10)
			sub.PeriodQuotaBytes = &v
		}
		out.Subscription = sub
		return nil
	})
	if err != nil {
		return ManagedUserDetail{}, err
	}
	return out, nil
}
func (s *UserReadService) Ledgers(ctx context.Context, p Principal, id int64, kind string, page, size int) (out ManagedLedgerPage, err error) {
	page, size, err = normalizeUserPage(page, size)
	if err != nil {
		return out, err
	}
	if id <= 0 || kind != "balance" && kind != "entitlement" {
		return out, httpx.NewError(httpx.ErrBadRequest, "用户或流水类型无效")
	}
	err = s.read(ctx, p, true, func(c context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NewError(httpx.ErrNotFound, "用户不存在")
		}
		out = ManagedLedgerPage{UserID: strconv.FormatInt(id, 10), Type: kind, Page: page, PageSize: size}
		var total int64
		if kind == "balance" {
			if err := tx.QueryRow(c, `SELECT count(*) FROM balance_ledger WHERE user_id=$1`, id).Scan(&total); err != nil {
				return err
			}
			rows, err := tx.Query(c, `SELECT l.id::text,l.entry_type,l.amount_micro,l.balance_after_micro,l.source_type,l.source_id::text,l.operator_type,l.created_at FROM balance_ledger l WHERE l.user_id=$1 ORDER BY l.created_at DESC,l.id DESC LIMIT $2 OFFSET $3`, id, size, (page-1)*size)
			if err != nil {
				return err
			}
			defer rows.Close()
			items := []ManagedBalanceEntry{}
			for rows.Next() {
				var e ManagedBalanceEntry
				var amount, after int64
				if err = rows.Scan(&e.ID, &e.EntryType, &amount, &after, &e.SourceType, &e.SourceID, &e.OperatorType, &e.CreatedAt); err != nil {
					return err
				}
				if after < 0 || amount == 0 {
					return httpx.Internal
				}
				e.Amount = money.Micro(amount).String()
				e.BalanceAfter = money.Micro(after).String()
				items = append(items, e)
			}
			if err = rows.Err(); err != nil {
				return err
			}
			out.Items = items
			currency := "USDT"
			out.Currency = &currency
		} else {
			if err := tx.QueryRow(c, `SELECT count(*) FROM subscription_ledger WHERE user_id=$1`, id).Scan(&total); err != nil {
				return err
			}
			rows, err := tx.Query(c, `SELECT l.id::text,l.change_type,l.delta_seconds::text,l.delta_bytes::text,l.delta_devices::text,l.source_type,l.source_id::text,l.operator_type,l.created_at FROM subscription_ledger l WHERE l.user_id=$1 ORDER BY l.created_at DESC,l.id DESC LIMIT $2 OFFSET $3`, id, size, (page-1)*size)
			if err != nil {
				return err
			}
			defer rows.Close()
			items := []ManagedEntitlementEntry{}
			for rows.Next() {
				var e ManagedEntitlementEntry
				if err = rows.Scan(&e.ID, &e.ChangeType, &e.DeltaSeconds, &e.DeltaBytes, &e.DeltaDevices, &e.SourceType, &e.SourceID, &e.OperatorType, &e.CreatedAt); err != nil {
					return err
				}
				items = append(items, e)
			}
			if err = rows.Err(); err != nil {
				return err
			}
			out.Items = items
		}
		out.Total = strconv.FormatInt(total, 10)
		return nil
	})
	if err != nil {
		return ManagedLedgerPage{}, err
	}
	return out, nil
}
