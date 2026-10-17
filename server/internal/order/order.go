// Package order 实现订单创建、唯一尾数分配、余额支付、查询与取消（方案 6.1–6.3、6.5、11.4）。
// 幂等：orders(user_id, client_idem_key)；报价：plan_quotes.used_by_order_id UNIQUE；金额槽位：amount_locks 主键。
package order

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/balance"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/money"
	"github.com/yuhai-vpn/server/internal/outbox"
	"github.com/yuhai-vpn/server/internal/plan"
)

const (
	OrderTTL        = 30 * time.Minute
	DefaultCooldown = 2 * time.Hour
	MaxPending      = 3
	TailAttempts    = 20
	TokenContract   = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" // TRON USDT
	defaultTopupMin = 1 * money.OneUSDT
	defaultTopupMax = 1000 * money.OneUSDT
)

type Service struct {
	Pool       *pgxpool.Pool
	Clock      clock.Clock
	Production bool // 生产环境只允许 TRON 收款地址
}

type CreateInput struct {
	OrderType string `json:"order_type"` // PLAN / TOPUP
	QuoteID   string `json:"quote_id"`
	Amount    string `json:"amount"`
	PayMethod string `json:"pay_method"` // USDT_TRC20 / BALANCE
}

type AddressProof struct {
	KeyID      string `json:"key_id"`
	ValidUntil int64  `json:"valid_until"`
	Signature  string `json:"signature"`
}

type Chain struct {
	TxHash     string    `json:"tx_hash"`
	DetectedAt time.Time `json:"detected_at"`
	Solidified bool      `json:"solidified"`
}

type View struct {
	OrderNo           string        `json:"order_no"`
	OrderType         string        `json:"order_type"`
	Status            string        `json:"status"`
	StatusHint        *string       `json:"status_hint"`
	PayMethod         string        `json:"pay_method"`
	PlanName          string        `json:"plan_name,omitempty"`
	BaseAmount        string        `json:"base_amount"`
	PayAmount         string        `json:"pay_amount"`
	Network           string        `json:"network,omitempty"`
	Token             string        `json:"token,omitempty"`
	TokenContract     string        `json:"token_contract,omitempty"`
	ReceiveAddress    string        `json:"receive_address,omitempty"`
	AddressProof      *AddressProof `json:"address_proof,omitempty"`
	Chain             *Chain        `json:"chain"`
	FulfillmentResult *string       `json:"fulfillment_result"`
	ExpiresAt         time.Time     `json:"expires_at"`
	PaidAt            *time.Time    `json:"paid_at"`
	CompletedAt       *time.Time    `json:"completed_at"`
	CreatedAt         time.Time     `json:"created_at"`
	ServerTime        time.Time     `json:"server_time"`
	etag              string
}

func (v View) ETag() string { return v.etag }

// ---------- 创建 ----------

func (s *Service) Create(ctx context.Context, userID int64, in CreateInput, idemKey string) (View, error) {
	now := s.Clock.Now()
	// 幂等：同一 client_idem_key 直接返回已有订单
	if existing, err := s.byIdem(ctx, userID, idemKey); err != nil {
		return View{}, err
	} else if existing != nil {
		return *existing, nil
	}
	if in.PayMethod != "USDT_TRC20" && in.PayMethod != "BALANCE" {
		return View{}, httpx.NewError(httpx.ErrBadRequest, "pay_method 无效")
	}
	var pending int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id = $1 AND status IN ('PENDING','CONFIRMING')`, userID).Scan(&pending); err != nil {
		return View{}, err
	}
	if in.PayMethod == "USDT_TRC20" && pending >= MaxPending {
		return View{}, httpx.NewError(httpx.ErrTooManyPending, "待支付订单过多，请先完成或取消已有订单")
	}

	var view View
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// Serialize the first payment with invite attribution before other locks.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "invite-payment:"+strconv.FormatInt(userID, 10)); err != nil {
			return err
		}
		// Refresh after waiting so an older request cannot backdate payment.
		now = s.Clock.Now()
		// A concurrent request may have completed while this one waited. Use
		// the same transaction and connection before touching a consumed quote.
		var existingNo string
		existingErr := tx.QueryRow(ctx, `SELECT order_no FROM orders WHERE user_id=$1 AND client_idem_key=$2`, userID, idemKey).Scan(&existingNo)
		if existingErr == nil {
			var err error
			view, err = s.loadTx(ctx, tx, existingNo, userID, now)
			return err
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return existingErr
		}
		var q plan.StoredQuote
		var planID *int64
		var quoteID *string
		var snapshot []byte
		var base money.Micro
		switch in.OrderType {
		case "PLAN":
			var err error
			if q, err = plan.LockQuote(ctx, tx, in.QuoteID, userID, now); err != nil {
				return err
			}
			if snapshot, err = q.Snapshot(); err != nil {
				return err
			}
			planID, quoteID, base = &q.PlanID, &q.ID, money.Micro(q.BaseMicro)
		case "TOPUP":
			if in.PayMethod != "USDT_TRC20" {
				return httpx.NewError(httpx.ErrBadRequest, "充值只能使用 USDT 支付")
			}
			m, err := money.Parse(in.Amount)
			if err != nil || m%money.TenthUSDT != 0 {
				return httpx.NewError(httpx.ErrAmountFormat, "充值金额须为 0.1 USDT 的整数倍")
			}
			lo, hi := s.topupRange(ctx)
			if m < lo || m > hi {
				return httpx.NewError(httpx.ErrTopupRange, "充值金额超出范围").WithDetails(map[string]any{"min": lo.String(), "max": hi.String()})
			}
			base = m
		default:
			return httpx.NewError(httpx.ErrBadRequest, "order_type 无效")
		}

		orderNo, err := ids.NewOrderNo(now)
		if err != nil {
			return err
		}
		expires := now.Add(OrderTTL)

		if in.PayMethod == "BALANCE" {
			id, err := s.insertOrder(ctx, tx, orderNo, userID, in.OrderType, planID, quoteID, snapshot, base, base, "BALANCE", nil, idemKey, expires)
			if err != nil {
				return err
			}
			if quoteID != nil {
				if err := plan.MarkQuoteUsed(ctx, tx, *quoteID, id); err != nil {
					return err
				}
			}
			if _, err := balance.Apply(ctx, tx, balance.Entry{
				UserID: userID, EntryType: "PURCHASE", Amount: -base, SourceType: "ORDER", SourceID: id,
				IdemKey: "purchase:order:" + strconv.FormatInt(id, 10), Operator: "USER", Remark: orderNo,
			}); errors.Is(err, balance.ErrInsufficient) {
				return httpx.NewError(httpx.ErrInsufficientFunds, "余额不足")
			} else if err != nil {
				return err
			}
			result, err := Fulfill(ctx, tx, id, userID, orderNo, q.Plan(), q.ChangeType(), base, now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE orders SET status = 'COMPLETED', fulfillment_result = $2, paid_at = $3, completed_at = $3, updated_at = $3 WHERE id = $1`, id, result, now); err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, outbox.OrderCompleted, "order", id, []byte(`{}`)); err != nil {
				return err
			}
			view, err = s.loadTx(ctx, tx, orderNo, userID, now)
			return err
		}

		// USDT：选地址 + 分配唯一尾数
		addr, err := s.pickAddress(ctx, tx)
		if err != nil {
			return err
		}
		payAmount, addrID, err := s.allocateTail(ctx, tx, addr, base, orderNo, expires)
		if err != nil {
			return err
		}
		id, err := s.insertOrder(ctx, tx, orderNo, userID, in.OrderType, planID, quoteID, snapshot, base, payAmount, "USDT_TRC20", &addrID, idemKey, expires)
		if err != nil {
			return err
		}
		if quoteID != nil {
			if err := plan.MarkQuoteUsed(ctx, tx, *quoteID, id); err != nil {
				return err
			}
		}
		view, err = s.loadTx(ctx, tx, orderNo, userID, now)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "orders_user_id_client_idem_key_key" {
			if existing, e2 := s.byIdem(ctx, userID, idemKey); e2 == nil && existing != nil {
				return *existing, nil
			}
		}
		return View{}, err
	}
	return view, nil
}

// Fulfill 开通订单权益；加购条件不满足时金额转入余额（方案 5.3）。返回 fulfillment_result。
// 到账事务（deposit.Settle）复用本函数。
func Fulfill(ctx context.Context, tx pgx.Tx, orderID, userID int64, orderNo string, p entitlement.Plan, changeType string, amount money.Micro, now time.Time) (string, error) {
	if p.ID == 0 { // TOPUP：入余额
		if _, err := balance.Apply(ctx, tx, balance.Entry{
			UserID: userID, EntryType: "DEPOSIT", Amount: amount, SourceType: "ORDER", SourceID: orderID,
			IdemKey: "fulfill:order:" + strconv.FormatInt(orderID, 10), Operator: "SYSTEM", Remark: orderNo,
		}); err != nil {
			return "", err
		}
		return "FULFILLED", nil
	}
	err := entitlement.Apply(ctx, tx, entitlement.Grant{
		UserID: userID, Plan: p, ChangeType: changeType, SourceType: "ORDER", SourceID: orderID,
		IdemKey: "fulfill:order:" + strconv.FormatInt(orderID, 10), Operator: "SYSTEM", Remark: orderNo,
	}, now)
	if errors.Is(err, entitlement.ErrNoActivePlan) {
		if _, err := balance.Apply(ctx, tx, balance.Entry{
			UserID: userID, EntryType: "FULFILL_FALLBACK", Amount: amount, SourceType: "ORDER", SourceID: orderID,
			IdemKey: "fulfill_fallback:order:" + strconv.FormatInt(orderID, 10), Operator: "SYSTEM", Remark: "套餐无法开通，金额转入余额",
		}); err != nil {
			return "", err
		}
		return "CREDITED_TO_BALANCE", nil
	}
	if err != nil {
		return "", err
	}
	return "FULFILLED", nil
}

type receiveAddress struct {
	ID         int64
	Network    string
	Address    string
	Signature  string
	KeyID      string
	ValidUntil time.Time
}

// pickAddress 选择负载最低的启用地址（按当前锁定槽位数升序）。
func (s *Service) pickAddress(ctx context.Context, tx pgx.Tx) ([]receiveAddress, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.id, a.network, a.address, a.signature, a.sig_key_id, a.valid_until
		  FROM receive_addresses a
		 WHERE a.status = 'ACTIVE' AND a.valid_until > now() AND ($1 = false OR a.network = 'TRON')
		 ORDER BY (SELECT count(*) FROM amount_locks l WHERE l.receive_address_id = a.id AND l.locked_until > now()), a.id`, s.Production)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []receiveAddress
	for rows.Next() {
		var a receiveAddress
		if err := rows.Scan(&a.ID, &a.Network, &a.Address, &a.Signature, &a.KeyID, &a.ValidUntil); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, httpx.NewError(httpx.ErrNoAmountSlot, "支付通道暂不可用，请稍后再试")
	}
	return out, rows.Err()
}

// allocateTail 方案 6.2：每地址最多 20 次随机尾数尝试，全部失败换下一地址；所有地址都满返回 50301。
func (s *Service) allocateTail(ctx context.Context, tx pgx.Tx, addrs []receiveAddress, base money.Micro, orderNo string, expires time.Time) (money.Micro, int64, error) {
	lockedUntil := expires.Add(s.cooldown(ctx))
	for _, a := range addrs {
		for i := 0; i < TailAttempts; i++ {
			tail, err := money.RandomTail()
			if err != nil {
				return 0, 0, err
			}
			amount := base + tail
			var got string
			err = tx.QueryRow(ctx, `
				INSERT INTO amount_locks (receive_address_id, amount_micro, order_no, locked_until)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (receive_address_id, amount_micro) DO UPDATE
				   SET order_no = EXCLUDED.order_no, locked_until = EXCLUDED.locked_until
				 WHERE amount_locks.locked_until < now()
				RETURNING order_no`, a.ID, int64(amount), orderNo, lockedUntil).Scan(&got)
			if err == nil {
				return amount, a.ID, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, 0, err
			}
		}
	}
	return 0, 0, httpx.NewError(httpx.ErrNoAmountSlot, "当前下单人数较多，请稍后再试")
}

func (s *Service) insertOrder(ctx context.Context, tx pgx.Tx, orderNo string, userID int64, orderType string, planID *int64, quoteID *string, snapshot []byte,
	base, pay money.Micro, payMethod string, addrID *int64, idemKey string, expires time.Time) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO orders (order_no, user_id, order_type, plan_id, quote_id, plan_snapshot, base_amount_micro, pay_amount_micro,
		                    pay_method, receive_address_id, client_idem_key, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		orderNo, userID, orderType, planID, quoteID, snapshot, int64(base), int64(pay), payMethod, addrID, idemKey, expires).Scan(&id)
	return id, err
}

// ---------- 查询 ----------

func (s *Service) Get(ctx context.Context, userID int64, orderNo string) (View, error) {
	now := s.Clock.Now()
	var v View
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		v, err = s.loadTx(ctx, tx, orderNo, userID, now)
		return err
	})
	return v, err
}

func (s *Service) byIdem(ctx context.Context, userID int64, idemKey string) (*View, error) {
	var orderNo string
	err := s.Pool.QueryRow(ctx, `SELECT order_no FROM orders WHERE user_id = $1 AND client_idem_key = $2`, userID, idemKey).Scan(&orderNo)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := s.Get(ctx, userID, orderNo)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Service) loadTx(ctx context.Context, tx pgx.Tx, orderNo string, userID int64, now time.Time) (View, error) {
	var v View
	var base, pay int64
	var snapshot []byte
	var network, address, sig, keyID *string
	var validUntil *time.Time
	var txHash *string
	var detectedAt *time.Time
	var solidified *bool
	var updatedAt time.Time
	var exceptionOpen bool
	err := tx.QueryRow(ctx, `
		SELECT o.order_no, o.order_type, o.status, o.pay_method, o.base_amount_micro, o.pay_amount_micro, o.plan_snapshot,
		       o.fulfillment_result, o.expires_at, o.paid_at, o.completed_at, o.created_at, o.updated_at,
		       a.network, a.address, a.signature, a.sig_key_id, a.valid_until,
		       c.tx_hash, c.created_at, (c.confirm_status = 'SOLIDIFIED'),
		       EXISTS (SELECT 1 FROM deposit_exceptions e WHERE e.suggested_order_id = o.id AND e.status = 'OPEN')
		  FROM orders o
		  LEFT JOIN receive_addresses a ON a.id = o.receive_address_id
		  LEFT JOIN chain_transfers c ON c.id = o.chain_transfer_id
		 WHERE o.order_no = $1 AND o.user_id = $2`, orderNo, userID).
		Scan(&v.OrderNo, &v.OrderType, &v.Status, &v.PayMethod, &base, &pay, &snapshot,
			&v.FulfillmentResult, &v.ExpiresAt, &v.PaidAt, &v.CompletedAt, &v.CreatedAt, &updatedAt,
			&network, &address, &sig, &keyID, &validUntil,
			&txHash, &detectedAt, &solidified, &exceptionOpen)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, httpx.NewError(httpx.ErrNotFound, "订单不存在")
	}
	if err != nil {
		return v, err
	}
	v.BaseAmount, v.PayAmount, v.ServerTime = money.Micro(base).String(), money.Micro(pay).String(), now
	if len(snapshot) > 0 {
		var d struct {
			Plan entitlement.Plan `json:"plan"`
		}
		if json.Unmarshal(snapshot, &d) == nil {
			v.PlanName = d.Plan.Name
		}
	}
	if v.PayMethod == "USDT_TRC20" && address != nil {
		v.Network, v.Token, v.TokenContract, v.ReceiveAddress = *network, "USDT", TokenContract, *address
		v.AddressProof = &AddressProof{KeyID: *keyID, ValidUntil: validUntil.Unix(), Signature: *sig}
	}
	if txHash != nil {
		v.Chain = &Chain{TxHash: *txHash, DetectedAt: *detectedAt, Solidified: solidified != nil && *solidified}
	}
	if exceptionOpen {
		h := "EXCEPTION_REVIEW"
		v.StatusHint = &h
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%v|%v", v.OrderNo, v.Status, updatedAt.UnixNano(), v.Chain != nil, exceptionOpen)))
	v.etag = `"` + hex.EncodeToString(sum[:8]) + `"`
	return v, nil
}

// List 游标分页（cursor = 上一页最后一条订单 id）。
func (s *Service) List(ctx context.Context, userID int64, cursor string, limit int) ([]View, string, error) {
	now := s.Clock.Now()
	var before int64 = 1 << 62
	if cursor != "" {
		b, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, "", httpx.NewError(httpx.ErrBadRequest, "cursor 无效")
		}
		before = b
	}
	rows, err := s.Pool.Query(ctx, `SELECT id, order_no FROM orders WHERE user_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, userID, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	type row struct {
		id int64
		no string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.no); err != nil {
			rows.Close()
			return nil, "", err
		}
		rs = append(rs, r)
	}
	rows.Close()
	next := ""
	if len(rs) > limit {
		next = strconv.FormatInt(rs[limit-1].id, 10)
		rs = rs[:limit]
	}
	out := []View{}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, r := range rs {
			v, err := s.loadTx(ctx, tx, r.no, userID, now)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out, next, err
}

// Cancel PENDING → CANCELLED；金额锁保留到冷却期结束（迟到付款进入异常队列而不是错配给他人）。
func (s *Service) Cancel(ctx context.Context, userID int64, orderNo string) (View, error) {
	now := s.Clock.Now()
	var v View
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE orders SET status = 'CANCELLED', updated_at = $3 WHERE order_no = $1 AND user_id = $2 AND status = 'PENDING'`, orderNo, userID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.loadTx(ctx, tx, orderNo, userID, now); err != nil {
				return err
			}
			return httpx.NewError(httpx.ErrOrderState, "订单状态不允许取消")
		}
		v, err = s.loadTx(ctx, tx, orderNo, userID, now)
		return err
	})
	return v, err
}

// ExpirePending 由 worker 调用：到期的 PENDING → EXPIRED。可重入。
func (s *Service) ExpirePending(ctx context.Context, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE orders SET status = 'EXPIRED', updated_at = $1 WHERE status = 'PENDING' AND expires_at < $1`, now)
	return err
}

// ---------- 系统参数 ----------

func (s *Service) cooldown(ctx context.Context) time.Duration {
	if v := s.configInt(ctx, "order.cooldown_seconds"); v > 0 {
		return time.Duration(v) * time.Second
	}
	return DefaultCooldown
}

func (s *Service) topupRange(ctx context.Context) (money.Micro, money.Micro) {
	lo, hi := money.Micro(defaultTopupMin), money.Micro(defaultTopupMax)
	if v := s.configInt(ctx, "topup.min_micro"); v > 0 {
		lo = money.Micro(v)
	}
	if v := s.configInt(ctx, "topup.max_micro"); v > 0 {
		hi = money.Micro(v)
	}
	return lo, hi
}

func (s *Service) configInt(ctx context.Context, key string) int64 {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT value FROM system_configs WHERE key = $1`, key).Scan(&raw); err != nil {
		return 0
	}
	var v int64
	if json.Unmarshal(raw, &v) != nil {
		return 0
	}
	return v
}
