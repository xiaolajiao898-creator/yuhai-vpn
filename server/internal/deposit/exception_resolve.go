package deposit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/balance"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

// 人工处置到账异常（方案 6.7）。
//
// Settle 在匹配不上时把转账丢进 deposit_exceptions 就收手了——**绝不自动入账到猜测的用户**。
// 在这之前，队列里的钱没有任何出路：金额差一点、付款晚了几分钟、地址付重了，
// 都只能停在那里，而用户的钱已经到了我们的地址上。
//
// 这个文件是那条出路，被 internal/admin 的 L3 双人审批接口调用。三种处置：
//
//   - TO_ORDER   把这笔钱记到指定订单上，走**与自动结算完全相同的那一段**（settleOnto）。
//   - TO_BALANCE 把钱记进指定用户的余额，不牵扯任何订单。
//   - REJECT     不动钱，只留档说明为什么不处理（例如地址投毒、第三方误转）。
//
// 三种都必须在调用方的事务里执行，并且**只处理 OPEN 的异常**：
// 条件更新影响行数不是 1 就整笔回滚。

// ManualAction 是三种人工处置。
const (
	ActionToOrder   = "TO_ORDER"
	ActionToBalance = "TO_BALANCE"
	ActionReject    = "REJECT"
)

var manualActions = map[string]string{
	ActionToOrder:   "RESOLVED_TO_ORDER",
	ActionToBalance: "RESOLVED_TO_BALANCE",
	ActionReject:    "REJECTED",
}

// ManualResolveInput 是一次人工处置的全部输入。
type ManualResolveInput struct {
	ExceptionID int64
	Action      string
	// OrderNo 仅 TO_ORDER 使用；UserAccountNo 仅 TO_BALANCE 使用。
	OrderNo       string
	UserAccountNo string
	Note          string
	// ApprovalID 与 OperatorID 写进留痕：谁批的、记在谁名下。
	ApprovalID int64
	OperatorID int64
	// ExpectAmountMicro 是发起审批时看到的转账金额，0 表示不校验。
	// 审批单可能在队列里放一天，执行前必须确认处置的还是当初那笔钱。
	ExpectAmountMicro int64
}

// ManualOutcome 如实描述处置结果，供审批留痕与审计使用。
//
// 里面几个「看起来多余」的字段其实是给人看的：AddressMatched 说明这笔钱付到的地址
// 是不是订单当初分配的那个（人工处置允许不一致，但不该让人事后才发现），
// AmountDelta 说明订单应付与实付差了多少。
type ManualOutcome struct {
	ExceptionID    int64
	TransferID     int64
	Status         string
	Reason         string
	AmountMicro    int64
	OrderNo        string
	UserID         int64
	UserAccountNo  string
	UserStatus     string
	Fulfillment    string
	AmountDelta    int64
	AddressMatched bool
}

// ResolveException 在调用方事务内执行一次人工处置。
func ResolveException(ctx context.Context, tx pgx.Tx, in ManualResolveInput, now time.Time) (
	ManualOutcome, error) {
	var out ManualOutcome
	status, ok := manualActions[in.Action]
	if !ok {
		return out, httpx.NewError(httpx.ErrBadRequest, "处置动作只能是 TO_ORDER、TO_BALANCE 或 REJECT")
	}
	if in.ExceptionID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "异常编号无效")
	}

	// ① 锁异常单。只处理 OPEN——已处置过的绝不允许翻案，那等于第二次动钱。
	var current string
	var transferID int64
	e := tx.QueryRow(ctx, `SELECT status,chain_transfer_id,reason FROM deposit_exceptions
 WHERE id=$1 FOR UPDATE`, in.ExceptionID).Scan(&current, &transferID, &out.Reason)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrNotFound, "到账异常不存在")
	}
	if e != nil {
		return out, e
	}
	if current != "OPEN" {
		return out, httpx.NewError(httpx.ErrOrderState, "该到账异常已处置过（"+current+"），不允许再次处理")
	}
	out.ExceptionID, out.TransferID, out.Status = in.ExceptionID, transferID, status

	// ② 锁转账。必须仍然是 SOLIDIFIED + EXCEPTION：
	// 若它已经被自动结算认领走（MATCHED），这笔钱已经有了归属，人工不能再记一次。
	var t transfer
	e = tx.QueryRow(ctx, `SELECT id,network,tx_hash,event_index,from_address,receive_address_id,
 amount_micro,block_time FROM chain_transfers
 WHERE id=$1 AND confirm_status='SOLIDIFIED' AND match_status='EXCEPTION' FOR UPDATE`, transferID).
		Scan(&t.ID, &t.Network, &t.TxHash, &t.EventIndex, &t.FromAddress, &t.ReceiveAddressID,
			&t.AmountMicro, &t.BlockTime)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrApprovalStale,
			"这笔链上转账已不在异常状态（可能已被自动结算认领），请重新核对")
	}
	if e != nil {
		return out, e
	}
	out.AmountMicro = t.AmountMicro
	if in.ExpectAmountMicro != 0 && in.ExpectAmountMicro != t.AmountMicro {
		return out, httpx.NewError(httpx.ErrApprovalStale, "转账金额与发起审批时不一致，请重新发起")
	}

	switch in.Action {
	case ActionReject:
		// 不动钱。转账保持 EXCEPTION：它确实没有被记到任何人头上，
		// 改成 MATCHED 会让对账以为它已经有了去处。

	case ActionToBalance:
		u, err := lockUserByAccountNo(ctx, tx, in.UserAccountNo)
		if err != nil {
			return out, err
		}
		out.UserID, out.UserAccountNo, out.UserStatus = u.id, u.accountNo, u.status
		// 幂等键用异常单编号而不是转账键：这笔钱是「按第 N 号异常的处置结论」记进去的。
		// 而「同一笔异常只处置一次」由上面的 OPEN 条件与下面的条件更新一起保证。
		if _, err = balance.Apply(ctx, tx, balance.Entry{
			UserID: u.id, EntryType: "EXCEPTION_CREDIT", Amount: money.Micro(t.AmountMicro),
			SourceType: "DEPOSIT_EXCEPTION", SourceID: in.ExceptionID,
			IdemKey:  "deposit_exception:" + strconv.FormatInt(in.ExceptionID, 10),
			Operator: "ADMIN", OperatorID: operatorPtr(in.OperatorID),
			Remark: "exception:" + strconv.FormatInt(in.ExceptionID, 10),
		}); err != nil {
			return out, err
		}

	case ActionToOrder:
		o, addressID, err := lockOrderForManualSettle(ctx, tx, in.OrderNo)
		if err != nil {
			return out, err
		}
		out.OrderNo, out.UserID = o.OrderNo, o.UserID
		out.AmountDelta = t.AmountMicro - o.Pay
		// 人工处置**允许**收款地址与订单当初分配的不一致（用户复用了旧地址是真实场景），
		// 但如实标出来：两位审批人看到的 payload 里有这一项。
		out.AddressMatched = addressID == t.ReceiveAddressID
		if out.Fulfillment, err = settleOnto(ctx, tx, t, o, now); err != nil {
			return out, err
		}
	}

	// ③ 收口。条件更新影响行数必须是 1，否则并发下可能已经有人处置过了。
	tag, e := tx.Exec(ctx, `UPDATE deposit_exceptions
 SET status=$2,resolution_note=$3,approval_id=$4,resolved_at=$5
 WHERE id=$1 AND status='OPEN'`,
		in.ExceptionID, status, truncateNote(in.Note), nullableID(in.ApprovalID), now)
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		return out, fmt.Errorf("到账异常 %d 条件更新影响 %d 行", in.ExceptionID, tag.RowsAffected())
	}
	return out, nil
}

type manualUser struct {
	id        int64
	accountNo string
	status    string
}

// lockUserByAccountNo 按账户号定位收款人。用账户号而不是内部编号，
// 是因为运维手上只有用户报给客服的那串数字，内部编号他们看不到也不该猜。
func lockUserByAccountNo(ctx context.Context, tx pgx.Tx, accountNo string) (manualUser, error) {
	var u manualUser
	if accountNo == "" {
		return u, httpx.NewError(httpx.ErrBadRequest, "转入余额必须给出 user_account_no")
	}
	err := tx.QueryRow(ctx, `SELECT id,account_no,status FROM users WHERE account_no=$1 FOR NO KEY UPDATE`,
		accountNo).Scan(&u.id, &u.accountNo, &u.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, httpx.NewError(httpx.ErrNotFound, "账户号不存在")
	}
	if err != nil {
		return u, err
	}
	// 已停用的账户照样入账：钱是他的，停用不代表没收。状态如实带出去给审批人看。
	return u, nil
}

// lockOrderForManualSettle 锁住目标订单并确认它还能接收付款。
// 返回订单与它当初分配的收款地址编号。
func lockOrderForManualSettle(ctx context.Context, tx pgx.Tx, orderNo string) (orderRow, int64, error) {
	var o orderRow
	var addressID *int64
	var payMethod string
	var transferID *int64
	if orderNo == "" {
		return o, 0, httpx.NewError(httpx.ErrBadRequest, "转入订单必须给出 order_no")
	}
	err := tx.QueryRow(ctx, `SELECT id,order_no,user_id,order_type,status,base_amount_micro,
 pay_amount_micro,plan_snapshot,created_at,pay_method,receive_address_id,chain_transfer_id
 FROM orders WHERE order_no=$1 FOR UPDATE`, orderNo).
		Scan(&o.ID, &o.OrderNo, &o.UserID, &o.Type, &o.Status, &o.Base, &o.Pay, &o.Snapshot,
			&o.CreatedAt, &payMethod, &addressID, &transferID)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, 0, httpx.NewError(httpx.ErrNotFound, "订单不存在")
	}
	if err != nil {
		return o, 0, err
	}
	if payMethod != "USDT_TRC20" {
		return o, 0, httpx.NewError(httpx.ErrOrderState, "只有链上支付的订单能接收到账处置")
	}
	if transferID != nil {
		return o, 0, httpx.NewError(httpx.ErrOrderState, "该订单已经关联了一笔链上转账")
	}
	// 与自动结算同一套可接收状态。COMPLETED/PAID 不在其中——那已经付过了，
	// 再记一笔就是重复入账。
	switch o.Status {
	case "PENDING", "CONFIRMING", "EXPIRED", "CANCELLED":
	default:
		return o, 0, httpx.NewError(httpx.ErrOrderState, "订单当前状态（"+o.Status+"）不能接收到账")
	}
	if addressID == nil {
		return o, 0, httpx.NewError(httpx.ErrOrderState, "该订单没有收款地址")
	}
	return o, *addressID, nil
}

func operatorPtr(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

func nullableID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// truncateNote 兜住 resolution_note 的 500 字符上限。调用方已经校验过长度，
// 这里是最后一道：处置记录写不进去比截断更糟。
func truncateNote(s string) string {
	r := []rune(s)
	if len(r) > 500 {
		return string(r[:500])
	}
	return s
}
