package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

// 资金只读（方案 11.5：orders / chain-transfers / deposit-exceptions /
// receive-addresses / scan-cursors）。
//
// 在此之前运维**看不到任何一笔订单或到账**——付款有没有到、卡在哪一步，只能查库。
//
// 三条贯穿本文件的规矩：
//
//  1. **金额一律是字符串**，固定 6 位小数，由 money.Micro 渲染。绝不用 JSON 数字：
//     数字在各语言里会被读成浮点，账目上这是不可接受的。
//  2. **只读，且只读需要的列。** 链上转账的 raw 原始载荷、收款地址的签名原文都不返回——
//     前者体量大且取决于数据源，后者是发给客户端验证归属的材料，运维核对用不到它，
//     放进列表只是多一个可泄露面。
//  3. **不替人下结论。** 扫描游标只如实返回，不判断「是否落后」；到账异常里的
//     suggested_order_no 明确标注为「疑似」，不代表系统已认定。
type FinanceReadService struct{ Auth *Service }

const financeRead = "finance.read"

type ManagedOrder struct {
	OrderNo       string     `json:"order_no"`
	UserID        string     `json:"user_id"`
	OrderType     string     `json:"order_type"`
	Status        string     `json:"status"`
	PayMethod     string     `json:"pay_method"`
	BaseAmount    string     `json:"base_amount"`
	PayAmount     string     `json:"pay_amount"`
	Currency      string     `json:"currency"`
	IsLatePayment bool       `json:"is_late_payment"`
	ExpiresAt     time.Time  `json:"expires_at"`
	PaidAt        *time.Time `json:"paid_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

type ManagedChainTransfer struct {
	ID            string    `json:"id"`
	Network       string    `json:"network"`
	TxHash        string    `json:"tx_hash"`
	FromAddress   string    `json:"from_address"`
	ToAddress     string    `json:"to_address"`
	Amount        string    `json:"amount"`
	Currency      string    `json:"currency"`
	BlockNumber   string    `json:"block_number"`
	BlockTime     time.Time `json:"block_time"`
	ConfirmStatus string    `json:"confirm_status"`
	MatchStatus   string    `json:"match_status"`
	OrderNo       *string   `json:"order_no"`
	CreatedAt     time.Time `json:"created_at"`
}

type OrderTimelineEntry struct {
	Event string    `json:"event"`
	At    time.Time `json:"at"`
}

type ManagedOrderPage struct {
	Items      []ManagedOrder `json:"items"`
	Total      string         `json:"total"`
	Page       int            `json:"page"`
	PageSize   int            `json:"page_size"`
	ObservedAt time.Time      `json:"observed_at"`
}

type ManagedOrderDetail struct {
	Order         ManagedOrder          `json:"order"`
	Timeline      []OrderTimelineEntry  `json:"timeline"`
	ChainTransfer *ManagedChainTransfer `json:"chain_transfer"`
	PlanSnapshot  json.RawMessage       `json:"plan_snapshot"`
	ObservedAt    time.Time             `json:"observed_at"`
}

type ManagedChainTransferPage struct {
	Items      []ManagedChainTransfer `json:"items"`
	Total      string                 `json:"total"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"page_size"`
	ObservedAt time.Time              `json:"observed_at"`
}

type ManagedDepositException struct {
	ID               string               `json:"id"`
	Reason           string               `json:"reason"`
	Status           string               `json:"status"`
	ChainTransfer    ManagedChainTransfer `json:"chain_transfer"`
	SuggestedOrderNo *string              `json:"suggested_order_no"`
	ResolutionNote   *string              `json:"resolution_note"`
	ResolvedAt       *time.Time           `json:"resolved_at"`
	CreatedAt        time.Time            `json:"created_at"`
}

type ManagedDepositExceptionPage struct {
	Items      []ManagedDepositException `json:"items"`
	Total      string                    `json:"total"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"page_size"`
	ObservedAt time.Time                 `json:"observed_at"`
}

type ManagedReceiveAddress struct {
	ID         string    `json:"id"`
	Network    string    `json:"network"`
	Address    string    `json:"address"`
	Label      *string   `json:"label"`
	SigKeyID   string    `json:"sig_key_id"`
	ValidUntil time.Time `json:"valid_until"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type ManagedReceiveAddressList struct {
	Items      []ManagedReceiveAddress `json:"items"`
	ObservedAt time.Time               `json:"observed_at"`
}

type ManagedScanCursor struct {
	ReceiveAddressID string     `json:"receive_address_id"`
	Network          string     `json:"network"`
	Address          string     `json:"address"`
	LastBlockNumber  *string    `json:"last_block_number"`
	LastBlockTime    time.Time  `json:"last_block_time"`
	LastSuccessAt    *time.Time `json:"last_success_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ManagedScanCursorList struct {
	Items      []ManagedScanCursor `json:"items"`
	ObservedAt time.Time           `json:"observed_at"`
}

type FinanceFilter struct {
	Page, PageSize int
	Status         string
	UserID         string
	OrderNo        string
	MatchStatus    string
	TxHash         string
}

// read 与节点只读同一套骨架：只读快照前后各实时核验一次会话与权限。
// 权限在查询过程中被撤销时，缓冲在内存里的这批行不得被交出去。
func (s *FinanceReadService) read(ctx context.Context, p Principal, fn func(context.Context, pgx.Tx, time.Time) error) error {
	return s.readWith(ctx, p, financeRead, fn)
}

// readWith 让同一套「快照前后各核验一次」的读取骨架服务于不同的只读能力
// （资金 finance.read、风控 risk.read）。权限码是参数，不是写死的常量——
// 否则每加一个只读域就要复制一遍这段，复制多了迟早有一份漏掉事后那次核验。
func (s *FinanceReadService) readWith(ctx context.Context, p Principal, permission string,
	fn func(context.Context, pgx.Tx, time.Time) error) error {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil {
		return httpx.Internal
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := s.authority(c, p, permission); err != nil {
		return err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollbackApproval(tx)
	if err = fn(c, tx, s.Auth.Clock.Now()); err != nil {
		return err
	}
	if err = tx.Commit(c); err != nil {
		return err
	}
	return s.authority(c, p, permission)
}

func (s *FinanceReadService) authority(ctx context.Context, p Principal, permission string) error {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return errSession
	}
	var allowed bool
	err = s.Auth.Pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM admin_users u JOIN admin_sessions session ON session.admin_user_id=u.id
 WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3)
 AND session.id=$2 AND session.revoked_at IS NULL AND session.expires_at>$3
 AND session.last_active_at>$3-interval '30 minutes'
 AND EXISTS(SELECT 1 FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id
   JOIN admin_permissions perm ON perm.code=rp.permission_code
   WHERE ur.admin_user_id=u.id AND perm.code=$4 AND perm.risk_level=0))`,
		id, p.SessionID, s.Auth.Clock.Now(), permission).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return httpx.NewError(httpx.ErrForbidden, "当前会话或查看权限已失效")
	}
	return nil
}

// amount 把 BIGINT 微单位渲染成固定 6 位小数的字符串。整个文件里金额只走这一条路。
func amount(micro int64) string { return money.Micro(micro).String() }

const orderColumns = `o.order_no,o.user_id::text,o.order_type,o.status,o.pay_method,
 o.base_amount_micro,o.pay_amount_micro,o.is_late_payment,o.expires_at,o.paid_at,o.completed_at,o.created_at`

func scanOrder(row pgx.Row) (ManagedOrder, error) {
	var o ManagedOrder
	var base, pay int64
	err := row.Scan(&o.OrderNo, &o.UserID, &o.OrderType, &o.Status, &o.PayMethod,
		&base, &pay, &o.IsLatePayment, &o.ExpiresAt, &o.PaidAt, &o.CompletedAt, &o.CreatedAt)
	if err != nil {
		return o, err
	}
	if _, err = positiveApprovalID(o.UserID); err != nil {
		return ManagedOrder{}, httpx.Internal
	}
	o.BaseAmount, o.PayAmount, o.Currency = amount(base), amount(pay), "USDT"
	return o, nil
}

func (s *FinanceReadService) Orders(ctx context.Context, p Principal, f FinanceFilter) (out ManagedOrderPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Status != "" && !validOrderStatus(f.Status) {
		return out, httpx.NewError(httpx.ErrBadRequest, "订单状态无效")
	}
	var userID int64
	if f.UserID != "" {
		if userID, err = positiveApprovalID(f.UserID); err != nil {
			return out, httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
		}
	}
	if len(f.OrderNo) > 24 {
		return out, httpx.NewError(httpx.ErrBadRequest, "订单号过长")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		// 订单号是精确匹配，不做模糊：后台按订单号查是为了核对具体一笔，
		// 模糊匹配只会把不相干的订单一起翻出来。
		// $2 必须显式声明为 bigint：与字面量 0 比较会让 PostgreSQL 把参数推断成 int4，
		// 而本项目的用户编号远超 int4 范围，真实数据一到就会报编码错误。
		const where = ` FROM orders o WHERE ($1='' OR o.status=$1)
 AND ($2::bigint=0 OR o.user_id=$2::bigint) AND ($3='' OR o.order_no=$3)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, f.Status, userID, f.OrderNo).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT `+orderColumns+where+
			` ORDER BY o.created_at DESC,o.id DESC LIMIT $4 OFFSET $5`,
			f.Status, userID, f.OrderNo, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedOrderPage{Items: []ManagedOrder{}, Total: strconv.FormatInt(total, 10),
			Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			o, e := scanOrder(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, o)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedOrderPage{}, err
	}
	return out, nil
}

func validOrderStatus(s string) bool {
	switch s {
	case "PENDING", "CONFIRMING", "PAID", "COMPLETED", "CANCELLED", "EXPIRED":
		return true
	}
	return false
}

func (s *FinanceReadService) Order(ctx context.Context, p Principal, orderNo string) (out ManagedOrderDetail, err error) {
	if orderNo == "" || len(orderNo) > 24 {
		return out, httpx.NewError(httpx.ErrBadRequest, "订单号无效")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		o, e := scanOrder(tx.QueryRow(c, `SELECT `+orderColumns+` FROM orders o WHERE o.order_no=$1`, orderNo))
		if errors.Is(e, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "订单不存在")
		}
		if e != nil {
			return e
		}
		out = ManagedOrderDetail{Order: o, Timeline: []OrderTimelineEntry{}, ObservedAt: now}
		// 时间线只列已经发生的节点，不预测未来状态。
		out.Timeline = append(out.Timeline, OrderTimelineEntry{Event: "CREATED", At: o.CreatedAt},
			OrderTimelineEntry{Event: "EXPIRES", At: o.ExpiresAt})
		if o.PaidAt != nil {
			out.Timeline = append(out.Timeline, OrderTimelineEntry{Event: "PAID", At: *o.PaidAt})
		}
		if o.CompletedAt != nil {
			out.Timeline = append(out.Timeline, OrderTimelineEntry{Event: "COMPLETED", At: *o.CompletedAt})
		}
		var snapshot []byte
		if e = tx.QueryRow(c, `SELECT plan_snapshot FROM orders WHERE order_no=$1`, orderNo).Scan(&snapshot); e != nil {
			return e
		}
		out.PlanSnapshot = snapshot

		t, e := scanChainTransfer(tx.QueryRow(c, `SELECT `+chainTransferColumns+chainTransferJoin+
			` WHERE t.id=(SELECT chain_transfer_id FROM orders WHERE order_no=$1)`, orderNo))
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		out.ChainTransfer = &t
		return nil
	})
	if err != nil {
		return ManagedOrderDetail{}, err
	}
	return out, nil
}

const chainTransferColumns = `t.id::text,t.network,t.tx_hash,t.from_address,t.to_address,t.amount_micro,
 t.block_number,t.block_time,t.confirm_status,t.match_status,o.order_no,t.created_at`
const chainTransferJoin = ` FROM chain_transfers t LEFT JOIN orders o ON o.id=t.order_id`

func scanChainTransfer(row pgx.Row) (ManagedChainTransfer, error) {
	var t ManagedChainTransfer
	var micro, block int64
	err := row.Scan(&t.ID, &t.Network, &t.TxHash, &t.FromAddress, &t.ToAddress, &micro,
		&block, &t.BlockTime, &t.ConfirmStatus, &t.MatchStatus, &t.OrderNo, &t.CreatedAt)
	if err != nil {
		return t, err
	}
	if _, err = positiveApprovalID(t.ID); err != nil {
		return ManagedChainTransfer{}, httpx.Internal
	}
	if block < 0 {
		return ManagedChainTransfer{}, httpx.Internal
	}
	t.Amount, t.Currency = amount(micro), "USDT"
	t.BlockNumber = strconv.FormatInt(block, 10)
	return t, nil
}

func (s *FinanceReadService) ChainTransfers(ctx context.Context, p Principal, f FinanceFilter) (out ManagedChainTransferPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.MatchStatus != "" && f.MatchStatus != "UNPROCESSED" && f.MatchStatus != "MATCHED" && f.MatchStatus != "EXCEPTION" {
		return out, httpx.NewError(httpx.ErrBadRequest, "匹配状态无效")
	}
	if len(f.TxHash) > 80 {
		return out, httpx.NewError(httpx.ErrBadRequest, "交易哈希过长")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` WHERE ($1='' OR t.match_status=$1) AND ($2='' OR t.tx_hash=$2)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+chainTransferJoin+where, f.MatchStatus, f.TxHash).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT `+chainTransferColumns+chainTransferJoin+where+
			` ORDER BY t.block_time DESC,t.id DESC LIMIT $3 OFFSET $4`,
			f.MatchStatus, f.TxHash, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedChainTransferPage{Items: []ManagedChainTransfer{}, Total: strconv.FormatInt(total, 10),
			Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			t, e := scanChainTransfer(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, t)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedChainTransferPage{}, err
	}
	return out, nil
}

var exceptionStatuses = map[string]bool{
	"OPEN": true, "RESOLVED_TO_ORDER": true, "RESOLVED_TO_BALANCE": true, "REJECTED": true,
}

func (s *FinanceReadService) DepositExceptions(ctx context.Context, p Principal, f FinanceFilter) (out ManagedDepositExceptionPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	// 契约原先写的是 OPEN / RESOLVED，而数据库的 CHECK 是
	// OPEN / RESOLVED_TO_ORDER / RESOLVED_TO_BALANCE / REJECTED——
	// 也就是说「按 RESOLVED 筛」**永远返回零条**，而且不报错，看起来只是「没有已处理的」。
	// 契约已按库改正（处置接口做出来之后这三个终态才真正会出现）。
	if f.Status != "" && !exceptionStatuses[f.Status] {
		return out, httpx.NewError(httpx.ErrBadRequest, "异常状态无效")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const from = ` FROM deposit_exceptions e JOIN chain_transfers t ON t.id=e.chain_transfer_id
 LEFT JOIN orders o ON o.id=t.order_id LEFT JOIN orders sug ON sug.id=e.suggested_order_id`
		const where = ` WHERE ($1='' OR e.status=$1)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+from+where, f.Status).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT e.id::text,e.reason,e.status,sug.order_no,e.resolution_note,
 e.resolved_at,e.created_at,`+chainTransferColumns+from+where+
			` ORDER BY e.created_at DESC,e.id DESC LIMIT $2 OFFSET $3`,
			f.Status, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedDepositExceptionPage{Items: []ManagedDepositException{},
			Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var x ManagedDepositException
			var t ManagedChainTransfer
			var micro, block int64
			if e = rows.Scan(&x.ID, &x.Reason, &x.Status, &x.SuggestedOrderNo, &x.ResolutionNote,
				&x.ResolvedAt, &x.CreatedAt,
				&t.ID, &t.Network, &t.TxHash, &t.FromAddress, &t.ToAddress, &micro,
				&block, &t.BlockTime, &t.ConfirmStatus, &t.MatchStatus, &t.OrderNo, &t.CreatedAt); e != nil {
				return e
			}
			if _, e = positiveApprovalID(x.ID); e != nil {
				return httpx.Internal
			}
			t.Amount, t.Currency = amount(micro), "USDT"
			t.BlockNumber = strconv.FormatInt(block, 10)
			x.ChainTransfer = t
			out.Items = append(out.Items, x)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedDepositExceptionPage{}, err
	}
	return out, nil
}

func (s *FinanceReadService) ReceiveAddresses(ctx context.Context, p Principal) (out ManagedReceiveAddressList, err error) {
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		// 注意这里**没有** signature 一列——见文件开头第 2 条。
		rows, e := tx.Query(c, `SELECT id::text,network,address,label,sig_key_id,valid_until,status,created_at
 FROM receive_addresses ORDER BY status,id LIMIT 501`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedReceiveAddressList{Items: []ManagedReceiveAddress{}, ObservedAt: now}
		for rows.Next() {
			var a ManagedReceiveAddress
			if e = rows.Scan(&a.ID, &a.Network, &a.Address, &a.Label, &a.SigKeyID,
				&a.ValidUntil, &a.Status, &a.CreatedAt); e != nil {
				return e
			}
			if _, e = positiveApprovalID(a.ID); e != nil {
				return httpx.Internal
			}
			// 宁可整体失败，也不返回一份被悄悄截断的收款地址列表。
			if len(out.Items) >= 500 {
				return httpx.NewError(httpx.ErrInternal, "收款地址数量超出读取上限")
			}
			out.Items = append(out.Items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedReceiveAddressList{}, err
	}
	return out, nil
}

func (s *FinanceReadService) ScanCursors(ctx context.Context, p Principal) (out ManagedScanCursorList, err error) {
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		rows, e := tx.Query(c, `SELECT c.receive_address_id::text,a.network,a.address,c.last_block_number,
 c.last_block_time,c.last_success_at,c.updated_at
 FROM chain_scan_cursors c JOIN receive_addresses a ON a.id=c.receive_address_id
 ORDER BY a.network,c.receive_address_id LIMIT 501`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedScanCursorList{Items: []ManagedScanCursor{}, ObservedAt: now}
		for rows.Next() {
			var v ManagedScanCursor
			var block *int64
			if e = rows.Scan(&v.ReceiveAddressID, &v.Network, &v.Address, &block,
				&v.LastBlockTime, &v.LastSuccessAt, &v.UpdatedAt); e != nil {
				return e
			}
			if _, e = positiveApprovalID(v.ReceiveAddressID); e != nil {
				return httpx.Internal
			}
			if block != nil {
				if *block < 0 {
					return httpx.Internal
				}
				s := strconv.FormatInt(*block, 10)
				v.LastBlockNumber = &s
			}
			if len(out.Items) >= 500 {
				return httpx.NewError(httpx.ErrInternal, "扫描游标数量超出读取上限")
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedScanCursorList{}, err
	}
	return out, nil
}
