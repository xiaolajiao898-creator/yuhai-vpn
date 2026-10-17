// Package deposit 实现链上转账入库与到账事务（方案 6.6–6.7、6.9）。
//
// Settle 是全系统最关键的事务，顺序固定：
//  1. FOR UPDATE 锁定 chain_transfers（已固化且未处理）
//  2. 按 (receive_address_id, pay_amount_micro, 时间窗) 匹配并锁定订单；无匹配 → deposit_exceptions
//  3. 条件更新订单 → PAID（影响行数必须为 1）
//  4. 余额先入（deposit:{network}:{tx}:{idx}）后扣（purchase:order:{id}，仅 PLAN）
//  5. 开通权益（fulfill:order:{id}），失败转余额（fulfill_fallback:order:{id}）
//  6. 订单 COMPLETED、转账 MATCHED、写 outbox ORDER_COMPLETED
//
// 不匹配 / 金额不符 / 迟到 / 重复付款 → 异常队列，绝不自动入账到猜测的用户。
package deposit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/balance"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
	"github.com/yuhai-vpn/server/internal/order"
	"github.com/yuhai-vpn/server/internal/outbox"
)

const (
	NetworkTron     = "TRON"
	NetworkMock     = "MOCK"
	nearMissPercent = 5      // 疑似订单：金额差 5% 以内
	minDepositMicro = 10_000 // < 0.01 USDT 不入库（地址投毒）
)

type Service struct {
	Pool       *pgxpool.Pool
	Clock      clock.Clock
	Production bool
	Cooldown   func(ctx context.Context) time.Duration
}

type transfer struct {
	ID               int64
	Network          string
	TxHash           string
	EventIndex       int
	FromAddress      string
	ReceiveAddressID int64
	AmountMicro      int64
	BlockTime        time.Time
}

type Result struct {
	TransferID int64  `json:"transfer_id"`
	Outcome    string `json:"outcome"` // MATCHED / EXCEPTION / SKIPPED
	OrderNo    string `json:"order_no,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Settle 处理一笔链上转账。可重入：已处理过直接返回 SKIPPED。
func (s *Service) Settle(ctx context.Context, transferID int64) (Result, error) {
	now := s.Clock.Now()
	cooldown := order.DefaultCooldown
	if s.Cooldown != nil {
		cooldown = s.Cooldown(ctx)
	}
	res := Result{TransferID: transferID, Outcome: "SKIPPED"}
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// ① 锁定转账
		var t transfer
		err := tx.QueryRow(ctx, `
			SELECT id, network, tx_hash, event_index, from_address, receive_address_id, amount_micro, block_time
			  FROM chain_transfers
			 WHERE id = $1 AND confirm_status = 'SOLIDIFIED' AND match_status = 'UNPROCESSED' FOR UPDATE`, transferID).
			Scan(&t.ID, &t.Network, &t.TxHash, &t.EventIndex, &t.FromAddress, &t.ReceiveAddressID, &t.AmountMicro, &t.BlockTime)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.Production && t.Network != NetworkTron {
			return fmt.Errorf("生产环境出现非 TRON 转账 id=%d", t.ID)
		}

		// ② 自动匹配只接受 [下单时间, 过期时间 + 冷却期) 内的转账。
		// 不依赖过期任务是否及时执行，也不向下单前回溯，避免金额槽位复用时串单。
		var o orderRow
		err = tx.QueryRow(ctx, `
			SELECT id, order_no, user_id, order_type, status, base_amount_micro, pay_amount_micro, plan_snapshot, created_at
			  FROM orders
			 WHERE pay_method = 'USDT_TRC20' AND receive_address_id = $1 AND pay_amount_micro = $2
			   AND created_at <= $3::timestamptz AND $3::timestamptz < expires_at + $4::interval
			   AND status IN ('PENDING', 'CONFIRMING', 'EXPIRED', 'CANCELLED')
			 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
			t.ReceiveAddressID, t.AmountMicro, t.BlockTime, cooldown).
			Scan(&o.ID, &o.OrderNo, &o.UserID, &o.Type, &o.Status, &o.Base, &o.Pay, &o.Snapshot, &o.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			reason, suggested := s.classify(ctx, tx, t, cooldown)
			res.Outcome, res.Reason = "EXCEPTION", reason
			return s.exception(ctx, tx, t.ID, reason, suggested, now)
		}
		if err != nil {
			return err
		}
		res.OrderNo = o.OrderNo
		// Record the actual guarded transition, not the request's queued time.
		now = s.Clock.Now()
		if _, err := settleOnto(ctx, tx, t, o, now); err != nil {
			return err
		}
		res.Outcome = "MATCHED"
		return nil
	})
	return res, err
}

// orderRow 是 settleOnto 需要的订单字段。字段与 Settle 里那个匿名结构体一一对应，
// 换成具名类型只是为了能把③–⑥这一段抽出来共用。
type orderRow struct {
	ID        int64
	OrderNo   string
	UserID    int64
	Type      string
	Status    string
	Base      int64
	Pay       int64
	Snapshot  []byte
	CreatedAt time.Time
}

// settleOnto 执行 Settle 的③–⑥：订单 → PAID → 资金流水（先入后扣）→ 开通权益 →
// COMPLETED、转账 MATCHED、写 outbox。返回 fulfillment_result。
//
// 抽出来是因为它现在有两个调用方：自动结算（Settle），以及后台人工处置
// （internal/admin 的「异常转入订单」，方案 6.7）。**这条路只能有一份实现**——
// 它决定谁的钱记到谁头上、权益发不发，两份实现迟早会分叉，而分叉的表现是账目对不上。
//
// 调用方必须已在同一事务里锁住这两行（转账 FOR UPDATE、订单 FOR UPDATE）。
func settleOnto(ctx context.Context, tx pgx.Tx, t transfer, o orderRow, now time.Time) (string, error) {
	// Invite binding takes this same guard and never takes an order lock.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("invite-payment:%d", o.UserID)); err != nil {
		return "", err
	}

	// ③ 条件更新 → PAID
	tag, err := tx.Exec(ctx, `
		UPDATE orders SET status = 'PAID', chain_transfer_id = $2,
		                  is_late_payment = (status IN ('EXPIRED','CANCELLED') OR $4::timestamptz >= expires_at),
		                  paid_at = $3, updated_at = $3
		 WHERE id = $1 AND status IN ('PENDING', 'CONFIRMING', 'EXPIRED', 'CANCELLED')`, o.ID, t.ID, now, t.BlockTime)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", fmt.Errorf("订单 %s 条件更新影响 %d 行", o.OrderNo, tag.RowsAffected())
	}

	// ④ 资金流水：先入后扣
	amount := money.Micro(t.AmountMicro)
	depositKey := fmt.Sprintf("deposit:%s:%s:%d", t.Network, t.TxHash, t.EventIndex)
	if _, err := balance.Apply(ctx, tx, balance.Entry{
		UserID: o.UserID, EntryType: "DEPOSIT", Amount: amount, SourceType: "CHAIN_TRANSFER", SourceID: t.ID,
		IdemKey: depositKey, Operator: "SYSTEM", Remark: o.OrderNo,
	}); err != nil {
		return "", err
	}
	result := "FULFILLED"
	if o.Type == "PLAN" {
		if _, err := balance.Apply(ctx, tx, balance.Entry{
			UserID: o.UserID, EntryType: "PURCHASE", Amount: -amount, SourceType: "ORDER", SourceID: o.ID,
			IdemKey: "purchase:order:" + strconv.FormatInt(o.ID, 10), Operator: "SYSTEM", Remark: o.OrderNo,
		}); err != nil {
			return "", err
		}
		// ⑤ 开通权益（从下单快照，不再读 plans）
		var snap struct {
			ChangeType string           `json:"change_type"`
			Plan       entitlement.Plan `json:"plan"`
		}
		if err := json.Unmarshal(o.Snapshot, &snap); err != nil {
			return "", fmt.Errorf("订单 %s 套餐快照损坏: %w", o.OrderNo, err)
		}
		if result, err = order.Fulfill(ctx, tx, o.ID, o.UserID, o.OrderNo, snap.Plan, snap.ChangeType, amount, now); err != nil {
			return "", err
		}
	}
	// TOPUP：④ 的 DEPOSIT 入账即为开通

	// ⑥ 收尾
	if _, err := tx.Exec(ctx, `UPDATE orders SET status = 'COMPLETED', fulfillment_result = $2, completed_at = $3, updated_at = $3 WHERE id = $1 AND status = 'PAID'`, o.ID, result, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE chain_transfers SET match_status = 'MATCHED', order_id = $2, processed_at = $3 WHERE id = $1`, t.ID, o.ID, now); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{
		"order_no": o.OrderNo, "user_id": o.UserID, "order_type": o.Type, "amount_micro": t.AmountMicro,
		"fulfillment_result": result, "network": t.Network, "tx_hash": t.TxHash, "from_address": t.FromAddress,
	})
	if err := outbox.Enqueue(ctx, tx, outbox.OrderCompleted, "order", o.ID, payload); err != nil {
		return "", err
	}
	return result, nil
}

// classify 给异常一个原因与疑似订单（同地址、金额差 5% 以内、时间接近；方案 6.7）。
func (s *Service) classify(ctx context.Context, tx pgx.Tx, t transfer, cooldown time.Duration) (string, *int64) {
	var id int64
	var status string
	var created, expires time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, status, created_at, expires_at FROM orders
		 WHERE pay_method = 'USDT_TRC20' AND receive_address_id = $1
		   AND abs(pay_amount_micro - $2) * 100 <= pay_amount_micro * $3
		   AND created_at BETWEEN $4::timestamptz - interval '2 days' AND $4::timestamptz + interval '2 minutes'
		 ORDER BY abs(pay_amount_micro - $2), created_at DESC LIMIT 1`, t.ReceiveAddressID, t.AmountMicro, nearMissPercent, t.BlockTime).
		Scan(&id, &status, &created, &expires)
	if err != nil {
		return "NO_MATCHING_ORDER", nil
	}
	switch {
	case status == "COMPLETED" || status == "PAID":
		return "DUPLICATE_PAYMENT", &id
	case t.BlockTime.Before(created):
		return "BEFORE_ORDER_CREATED", &id
	case !t.BlockTime.Before(expires.Add(cooldown)):
		return "LATE_BEYOND_COOLDOWN", &id
	default:
		return "AMOUNT_MISMATCH", &id
	}
}

func (s *Service) exception(ctx context.Context, tx pgx.Tx, transferID int64, reason string, suggested *int64, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO deposit_exceptions (chain_transfer_id, reason, suggested_order_id) VALUES ($1, $2, $3)
		ON CONFLICT (chain_transfer_id) DO NOTHING`, transferID, reason, suggested); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE chain_transfers SET match_status = 'EXCEPTION', processed_at = $2 WHERE id = $1`, transferID, now)
	return err
}

// SettlePending 由 worker 调用：处理所有已固化未处理的转账。
func (s *Service) SettlePending(ctx context.Context, _ time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT id FROM chain_transfers WHERE confirm_status = 'SOLIDIFIED' AND match_status = 'UNPROCESSED' ORDER BY id LIMIT 200`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var firstErr error
	for _, id := range ids {
		if _, err := s.Settle(ctx, id); err != nil && firstErr == nil {
			firstErr = err // 单笔失败不阻塞其余，下一轮重试
		}
	}
	return firstErr
}

// ---------- 阶段一：模拟到账 ----------

type MockInput struct {
	OrderNo     string `json:"order_no"`
	Amount      string `json:"amount"`
	FromAddress string `json:"from_address"`
}

// MockDeposit 构造 network=MOCK 的转账记录，然后走与生产完全相同的 Settle。生产环境拒绝。
func (s *Service) MockDeposit(ctx context.Context, in MockInput) (Result, error) {
	if s.Production {
		return Result{}, httpx.NewError(httpx.ErrForbidden, "生产环境禁止模拟到账")
	}
	amount, err := money.Parse(in.Amount)
	if err != nil {
		return Result{}, httpx.NewError(httpx.ErrAmountFormat, "金额格式错误")
	}
	if amount < minDepositMicro {
		return Result{}, httpx.NewError(httpx.ErrAmountFormat, "低于 0.01 USDT 的转账不入库")
	}
	var addrID int64
	var addr string
	err = s.Pool.QueryRow(ctx, `
		SELECT a.id, a.address FROM orders o JOIN receive_addresses a ON a.id = o.receive_address_id
		 WHERE o.order_no = $1 AND o.pay_method = 'USDT_TRC20'`, in.OrderNo).Scan(&addrID, &addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, httpx.NewError(httpx.ErrNotFound, "订单不存在或不是 USDT 订单")
	}
	if err != nil {
		return Result{}, err
	}
	from := in.FromAddress
	if from == "" {
		from = "MOCK_PAYER"
	}
	now := s.Clock.Now()
	var id int64
	if err := s.Pool.QueryRow(ctx, `
		INSERT INTO chain_transfers (network, tx_hash, event_index, contract_address, from_address, to_address, receive_address_id,
		                             amount_micro, block_number, block_time, confirm_status, data_source, raw)
		VALUES ('MOCK', $1, 0, 'MOCK', $2, $3, $4, $5, 0, $6, 'SOLIDIFIED', 'MOCK', $7) RETURNING id`,
		"mock-"+uuid.NewString(), from, addr, addrID, int64(amount), now, []byte(`{"mock":true}`)).Scan(&id); err != nil {
		return Result{}, err
	}
	return s.Settle(ctx, id)
}

// ---------- 对账（方案 10.5、12.3） ----------

type ReconcileReport struct {
	BalanceMismatch []string `json:"balance_mismatch"`  // user_id 列表
	StuckTransfers  []int64  `json:"stuck_transfers"`   // 固化超过 10 分钟仍未处理
	OrdersNoFulfill []string `json:"orders_no_fulfill"` // COMPLETED 却没有 fulfill 流水（PLAN）
	OK              bool     `json:"ok"`
}

// Reconcile 汇总方案 10.5 的三类对账异常。
//
// 三次 Scan 与三次 rows.Err() 的错误此前都被丢掉了（`_ = rows.Scan(...)`）：
// 查询报错时 OK 会照样算成 true，对账结果反而比真的干净——一个只在出问题时说谎的报告。
// 后台导出（internal/admin 的 RECONCILE）用的是同一套口径，所以这里先改对。
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var r ReconcileReport
	rows, err := s.Pool.Query(ctx, `
		SELECT b.user_id FROM user_balances b LEFT JOIN balance_ledger l ON l.user_id = b.user_id
		 GROUP BY b.user_id, b.available_micro HAVING b.available_micro <> COALESCE(SUM(l.amount_micro), 0)`)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return r, err
		}
		r.BalanceMismatch = append(r.BalanceMismatch, strconv.FormatInt(id, 10))
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return r, err
	}
	rows, err = s.Pool.Query(ctx, `SELECT id FROM chain_transfers WHERE confirm_status = 'SOLIDIFIED' AND match_status = 'UNPROCESSED' AND created_at < now() - interval '10 minutes'`)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return r, err
		}
		r.StuckTransfers = append(r.StuckTransfers, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return r, err
	}
	rows, err = s.Pool.Query(ctx, `
		SELECT o.order_no FROM orders o
		 WHERE o.status = 'COMPLETED' AND o.order_type = 'PLAN'
		   AND NOT EXISTS (SELECT 1 FROM subscription_ledger l WHERE l.idempotency_key = 'fulfill:order:' || o.id)
		   AND NOT EXISTS (SELECT 1 FROM balance_ledger b WHERE b.idempotency_key = 'fulfill_fallback:order:' || o.id)`)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var no string
		if err = rows.Scan(&no); err != nil {
			rows.Close()
			return r, err
		}
		r.OrdersNoFulfill = append(r.OrdersNoFulfill, no)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return r, err
	}
	r.OK = len(r.BalanceMismatch) == 0 && len(r.StuckTransfers) == 0 && len(r.OrdersNoFulfill) == 0
	return r, nil
}
