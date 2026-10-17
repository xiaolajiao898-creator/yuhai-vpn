package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/deposit"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

// 到账异常处置（admin.yaml POST /deposit-exceptions/{id}/resolve）。
//
// 在这之前，`GET /deposit-exceptions` 能看见队列，但**队列里的钱没有任何出路**：
// 金额差一点、付款晚了几分钟、同一个地址被付了两次，转账就停在 EXCEPTION 上，
// 而用户的钱已经到了我们的地址。这一节把出路接上。
//
// 这是全项目第一个真正**动别人的钱**的后台接口，所以每一层都往严里做：
//
//   - L3 双人审批。发起只建审批单，钱在另一名管理员点通过时才动（执行器 executeExceptionResolve）。
//   - 记账那一段**不在这里实现**。TO_ORDER 走的是 internal/deposit 的 settleOnto，
//     与自动结算逐行相同的那一段；这条路只能有一份实现，两份迟早分叉，
//     而分叉的表现是账目对不上。
//   - 审批 payload 里带上发起时看到的金额，执行前重新核对，对不上返回 40908。
//     审批单可以在队列里放一天，这一天里那笔转账可能已经被自动结算认领走了。
//   - 只处理 OPEN 的异常，条件更新影响行数必须是 1。已处置的绝不允许翻案。

const (
	actionExceptionResolve = "finance.deposit_exception"
	exceptionResolveTarget = "deposit_exception"
)

type ExceptionResolveInput struct {
	Action        string `json:"action"`
	OrderNo       string `json:"order_no"`
	UserAccountNo string `json:"user_account_no"`
	Note          string `json:"note"`
}

// exceptionResolvePayload 是存进审批单的那份内容。
//
// 它不只是把请求抄一遍：`amount` 与 `reason` 是**发起时系统看到的事实**，
// 两位审批人据此判断；执行时会重新核对，不一致就拒绝执行。
// 金额用固定 6 位小数的字符串，与所有资金投影同一口径。
type exceptionResolvePayload struct {
	ExceptionID   string `json:"exception_id"`
	TransferID    string `json:"transfer_id"`
	Reason        string `json:"reason"`
	Amount        string `json:"amount"`
	AmountMicro   string `json:"amount_micro"`
	Action        string `json:"action"`
	OrderNo       string `json:"order_no,omitempty"`
	UserAccountNo string `json:"user_account_no,omitempty"`
	Note          string `json:"note"`
}

// DepositExceptionService 发起到账异常处置审批。
type DepositExceptionService struct{ Security *ApprovalService }

// RequestResolve 发起一张到账异常处置审批单。
//
// 这里做的校验与执行时做的**是同一套**（动作合法、异常存在且 OPEN、转账仍在 EXCEPTION、
// 目标订单/账户存在且可接收）。发起时校验是为了不让明显不成立的申请占着审批队列；
// 执行时再校验是因为那才是真正动钱的一刻。
func (s *DepositExceptionService) RequestResolve(ctx context.Context, p ApprovalProof,
	exceptionID int64, in ExceptionResolveInput) (Approval, error) {
	if s == nil || s.Security == nil {
		return Approval{}, httpx.Internal
	}
	e := s.Security.auditEvent(p, actionExceptionResolve, exceptionResolveTarget, exceptionID)
	return s.Security.mutate(ctx, p, e, func(ctx context.Context, tx pgx.Tx, event *audit.Event) (Approval, error) {
		var out Approval
		actor, err := positiveApprovalID(p.Principal.ID)
		if err != nil {
			return out, errSession
		}
		if exceptionID <= 0 {
			return out, httpx.NewError(httpx.ErrBadRequest, "异常编号无效")
		}
		payload, hash, err := s.buildPayload(ctx, tx, exceptionID, in)
		if err != nil {
			return out, err
		}
		if err = lockApprovalUsers(ctx, tx, actor, actor); err != nil {
			return out, err
		}
		if err = s.Security.Auth.ConsumeStepUp(ctx, tx, p.Principal, p.Token, actionExceptionResolve,
			p.RequestHash, p.IP); err != nil {
			return out, err
		}
		event.StepUpVerified = true
		if err = lockApprovalPermission(ctx, tx, actor, actionExceptionResolve, 3,
			s.Security.Auth.Clock.Now()); err != nil {
			return out, err
		}
		// 同一笔异常同时只允许一张待审批单。**不按 payload_hash 去重**——
		// 换个处置方式（TO_ORDER 改成 REJECT）是另一件事，不该被当成重复提交而悄悄复用旧单；
		// 但也不该让同一笔钱上挂着两张互相矛盾的待审批单。
		previous, err := scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+`
 FROM admin_approvals WHERE action_code=$1 AND target_type=$2 AND target_id=$3
 AND status='PENDING' ORDER BY id LIMIT 1 FOR UPDATE`,
			actionExceptionResolve, exceptionResolveTarget, exceptionID))
		switch {
		case err == nil && previous.ExpiresAt.After(s.Security.Auth.Clock.Now()):
			if previous.PayloadHash == hash {
				if previous.RequestedBy != p.Principal.ID {
					return out, httpx.NewError(httpx.ErrConflict, "该异常已有其他管理员发起的相同申请")
				}
				out = previous
			} else {
				return out, httpx.NewError(httpx.ErrConflict,
					"该异常已有一张待审批的处置申请，请先让它被审批或过期")
			}
		case err == nil:
			if _, err = tx.Exec(ctx, `UPDATE admin_approvals SET status='EXPIRED'
 WHERE id=$1 AND status='PENDING'`, previous.ID); err != nil {
				return out, err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return out, err
		}
		if err = s.Security.freshProof(ctx, tx, p); err != nil {
			return out, err
		}
		if out.ID == "" {
			now := s.Security.Auth.Clock.Now()
			out, err = scanApproval(tx.QueryRow(ctx, `INSERT INTO admin_approvals(action_code,
 target_type,target_id,payload,payload_hash,reason,requested_by,requested_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+approvalColumns,
				actionExceptionResolve, exceptionResolveTarget, exceptionID, payload, hash,
				in.Note, actor, now, now.Add(approvalTTL)))
			if err != nil {
				return out, err
			}
		}
		approvalID, _ := positiveApprovalID(out.ID)
		event.ApprovalID = &approvalID
		event.AfterData = approvalAuditData(out, false)
		return out, nil
	})
}

// buildPayload 校验输入并把「发起时系统看到的事实」固定进 payload。
func (s *DepositExceptionService) buildPayload(ctx context.Context, tx pgx.Tx, exceptionID int64,
	in ExceptionResolveInput) ([]byte, string, error) {
	bad := func(msg string) ([]byte, string, error) {
		return nil, "", httpx.NewError(httpx.ErrBadRequest, msg)
	}
	switch in.Action {
	case deposit.ActionToOrder, deposit.ActionToBalance, deposit.ActionReject:
	default:
		return bad("处置动作只能是 TO_ORDER、TO_BALANCE 或 REJECT")
	}
	// 与所有 L3 动作的 reason 共用 approvalText（上限 500 个可见字符，
	// 正好是 deposit_exceptions.resolution_note 与 admin_approvals.reason 的列宽）。
	// 不为这一个字段另立一套长度规矩——两套长度规矩迟早会有一套没人记得。
	if !approvalText(in.Note) {
		return bad("处置说明必须是 1–500 个可见字符")
	}
	// 不属于该动作的字段一律拒绝，不静默忽略：一张写着 order_no 的 REJECT 审批单，
	// 审批人多半会以为钱进了那张订单。
	if in.Action != deposit.ActionToOrder && in.OrderNo != "" {
		return bad("只有 TO_ORDER 才接受 order_no")
	}
	if in.Action != deposit.ActionToBalance && in.UserAccountNo != "" {
		return bad("只有 TO_BALANCE 才接受 user_account_no")
	}
	if in.Action == deposit.ActionToOrder && !nodeText(in.OrderNo, 24, false) {
		return bad("order_no 无效")
	}
	if in.Action == deposit.ActionToBalance && !nodeText(in.UserAccountNo, 10, false) {
		return bad("user_account_no 无效")
	}

	var status, reason string
	var transferID, amountMicro int64
	var matchStatus string
	err := tx.QueryRow(ctx, `SELECT e.status,e.reason,t.id,t.amount_micro,t.match_status
 FROM deposit_exceptions e JOIN chain_transfers t ON t.id=e.chain_transfer_id
 WHERE e.id=$1`, exceptionID).Scan(&status, &reason, &transferID, &amountMicro, &matchStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", httpx.NewError(httpx.ErrNotFound, "到账异常不存在")
	}
	if err != nil {
		return nil, "", err
	}
	if status != "OPEN" {
		return nil, "", httpx.NewError(httpx.ErrOrderState,
			"该到账异常已处置过（"+status+"），不允许再次处理")
	}
	if matchStatus != "EXCEPTION" {
		return nil, "", httpx.NewError(httpx.ErrOrderState,
			"这笔链上转账已不在异常状态，无需人工处置")
	}
	// 目标存不存在、能不能接收，发起时就查一遍——让明显不成立的申请当场失败，
	// 而不是排到审批人面前才发现。这里只读不锁：真正的锁定在执行时。
	switch in.Action {
	case deposit.ActionToOrder:
		var payMethod, orderStatus string
		var linked *int64
		e := tx.QueryRow(ctx, `SELECT pay_method,status,chain_transfer_id FROM orders
 WHERE order_no=$1`, in.OrderNo).Scan(&payMethod, &orderStatus, &linked)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, "", httpx.NewError(httpx.ErrNotFound, "订单不存在")
		}
		if e != nil {
			return nil, "", e
		}
		if payMethod != "USDT_TRC20" {
			return nil, "", httpx.NewError(httpx.ErrOrderState, "只有链上支付的订单能接收到账处置")
		}
		if linked != nil {
			return nil, "", httpx.NewError(httpx.ErrOrderState, "该订单已经关联了一笔链上转账")
		}
		switch orderStatus {
		case "PENDING", "CONFIRMING", "EXPIRED", "CANCELLED":
		default:
			return nil, "", httpx.NewError(httpx.ErrOrderState,
				"订单当前状态（"+orderStatus+"）不能接收到账")
		}
	case deposit.ActionToBalance:
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE account_no=$1)`,
			in.UserAccountNo).Scan(&exists); e != nil {
			return nil, "", e
		}
		if !exists {
			return nil, "", httpx.NewError(httpx.ErrNotFound, "账户号不存在")
		}
	}

	payload := exceptionResolvePayload{
		ExceptionID: strconv.FormatInt(exceptionID, 10),
		TransferID:  strconv.FormatInt(transferID, 10),
		Reason:      reason,
		Amount:      money.Micro(amountMicro).String(),
		AmountMicro: strconv.FormatInt(amountMicro, 10),
		Action:      in.Action,
		OrderNo:     in.OrderNo, UserAccountNo: in.UserAccountNo, Note: in.Note,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	return raw, payloadDigest(raw), nil
}

// payloadDigest 与地址包导入用同一种做法：序列化后的字节做 SHA-256。
// payload 本身已经是规范形态（字段全是字符串、顺序由结构体定死），不需要额外规范化。
func payloadDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// executeExceptionResolve 是 finance.deposit_exception 的执行器：审批通过的同一事务里动钱。
//
// 这里把 payload 原样解出来重算一遍校验和：与审批时对不上就是被改过。
// 然后把「发起时看到的金额」作为 ExpectAmountMicro 交给 internal/deposit——
// 那一天里转账可能已经被自动结算认领走了，金额也可能因为重新固化而变。
func executeExceptionResolve(ctx context.Context, s *ApprovalService, tx pgx.Tx, p ApprovalProof,
	out Approval, approvalID int64, now time.Time) error {
	var payload exceptionResolvePayload
	if len(out.payload) > 16*1024 || decodeApprovalObject(out.payload, &payload) != nil {
		return httpx.NewError(httpx.ErrApprovalStale, "处置申请已损坏，请重新发起")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if payloadDigest(raw) != out.PayloadHash {
		return httpx.NewError(httpx.ErrApprovalStale, "处置申请与审批时不一致，请重新发起")
	}
	exceptionID, err := positiveApprovalID(payload.ExceptionID)
	if err != nil || out.TargetID == nil || *out.TargetID != payload.ExceptionID {
		// payload 里的异常编号必须与审批单的 target_id 一致。
		// 不一致意味着有人把一张审批单的目标和内容拆开了。
		return httpx.NewError(httpx.ErrApprovalStale, "处置申请的目标与内容不一致，请重新发起")
	}
	expect, err := strconv.ParseInt(payload.AmountMicro, 10, 64)
	if err != nil || expect <= 0 {
		return httpx.NewError(httpx.ErrApprovalStale, "处置申请的金额已损坏，请重新发起")
	}
	if err = s.freshProof(ctx, tx, p); err != nil {
		return err
	}
	if !out.ExpiresAt.After(now) {
		return httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
	}
	actor, err := positiveApprovalID(p.Principal.ID)
	if err != nil {
		return errSession
	}
	_, err = deposit.ResolveException(ctx, tx, deposit.ManualResolveInput{
		ExceptionID: exceptionID, Action: payload.Action,
		OrderNo: payload.OrderNo, UserAccountNo: payload.UserAccountNo, Note: payload.Note,
		ApprovalID: approvalID, OperatorID: actor, ExpectAmountMicro: expect,
	}, now)
	return err
}
