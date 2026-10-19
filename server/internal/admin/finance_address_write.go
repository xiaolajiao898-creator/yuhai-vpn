package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 资金地址的两个写操作（admin.yaml POST /receive-addresses/{id}/status 与
// GET|POST /shared-pay-addresses）。
//
// 两张表此前都只能靠手写 SQL 维护，而它们各自都直接影响一条线上链路：
//
//   - `receive_addresses.status` 决定 internal/order 的 pickAddress 会不会把它分给新订单。
//     **把最后一个可用地址停掉，下单会直接返回 50301「支付通道暂不可用」**——
//     这是这一节最需要挡住的那把脚枪。
//   - `shared_pay_addresses` 的唯一读取方是 internal/invite/risk_rewards.go：
//     从命中它的地址付的款，不会被当成自邀请的铁证，而是转人工复核。
//     登记错一个地址，影响的是别人的奖励能不能发。
type FinanceAddressService struct{ Security *ApprovalService }

const (
	actionAddressStatus = "finance.address_status"
	actionSharedPay     = "finance.shared_pay_write"
)

var receiveAddressStatuses = map[string]bool{"ACTIVE": true, "PAUSED": true, "RETIRED": true}

type ReceiveAddressStatusInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	Status      string `json:"status"`
	// ConfirmLast 是对「停用后就没有可用收款地址了」的显式确认。
	// 不默认放行：全站停收款是个正当操作，但绝不该在点错一个按钮时悄悄发生。
	ConfirmLast bool `json:"confirm_last_address"`
}

type ManagedReceiveAddressStatus struct {
	ID         string    `json:"id"`
	Network    string    `json:"network"`
	Address    string    `json:"address"`
	Status     string    `json:"status"`
	PrevStatus string    `json:"previous_status"`
	ValidUntil time.Time `json:"valid_until"`
	// ActiveRemaining 是本次改动之后仍然可用（ACTIVE 且未过期）的收款地址数量。
	// 原样给出，不做判断——0 意味着新订单会失败，调用方应当自己看得见这个数。
	ActiveRemaining string    `json:"active_remaining"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type SharedPayAddressInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	Address     string `json:"address"`
	Note        string `json:"note"`
}

type ManagedSharedPayAddress struct {
	Address string  `json:"address"`
	Note    *string `json:"note"`
	// AutoDetected 区分「扫描自动识别」与「人工登记」。本接口写入的恒为 false——
	// 两者混在一起，事后就说不清某个地址是谁认定的。
	AutoDetected bool      `json:"auto_detected"`
	CreatedAt    time.Time `json:"created_at"`
}

type ManagedSharedPayAddressPage struct {
	Items      []ManagedSharedPayAddress `json:"items"`
	Total      string                    `json:"total"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"page_size"`
	ObservedAt time.Time                 `json:"observed_at"`
}

// SetReceiveAddressStatus 启用或停用一个收款地址。
//
// 状态只影响**新订单的分配**：已经发给用户的收款地址不会失效，链上扫描也照常进行。
// 停用不是「停止收款」，更不是删除——orders / chain_transfers 都外键引用这张表。
func (s *FinanceAddressService) SetReceiveAddressStatus(ctx context.Context, p ApprovalProof,
	addressID int64, in ReceiveAddressStatusInput) (out ManagedReceiveAddressStatus, err error) {
	if s == nil || s.Security == nil {
		return out, httpx.Internal
	}
	if addressID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "地址编号无效")
	}
	if !receiveAddressStatuses[in.Status] {
		return out, httpx.NewError(httpx.ErrBadRequest, "状态只能是 ACTIVE、PAUSED 或 RETIRED")
	}

	raw, err := securedMutation(ctx, s.Security, "admin_finance_mutations", "admin-finance-write",
		p, actionAddressStatus, "receive_address", addressID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var entry ManagedReceiveAddressStatus
			e := tx.QueryRow(c, `SELECT id::text,network,address,status,valid_until
 FROM receive_addresses WHERE id=$1 FOR UPDATE`, addressID).
				Scan(&entry.ID, &entry.Network, &entry.Address, &entry.PrevStatus, &entry.ValidUntil)
			if errors.Is(e, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "收款地址不存在")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			// 启用一个已过期的地址没有意义：pickAddress 要求 valid_until > now()，
			// 它会立刻被跳过。与其让人以为启用成功了，不如直接说清楚。
			if in.Status == "ACTIVE" && !entry.ValidUntil.After(now) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrBadRequest,
					"该地址的有效期已过，启用后仍不会被分配；请先更新地址有效期")
			}
			if _, e = tx.Exec(c, `UPDATE receive_addresses SET status=$2,updated_at=$3 WHERE id=$1`,
				addressID, in.Status, now); e != nil {
				return 0, nil, nil, nil, e
			}
			// 改完之后再数一次：这就是新订单实际能用的地址数。
			var remaining int64
			if e = tx.QueryRow(c, `SELECT count(*) FROM receive_addresses
 WHERE status='ACTIVE' AND valid_until>$1`, now).Scan(&remaining); e != nil {
				return 0, nil, nil, nil, e
			}
			if remaining == 0 && !in.ConfirmLast {
				// 整笔回滚：securedMutation 在返回错误时不会提交。
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict,
					"这是最后一个可用收款地址，停用后新订单会直接失败（50301）；"+
						"确实要全站停收款请带上 confirm_last_address=true")
			}
			entry.Status, entry.UpdatedAt = in.Status, now
			entry.ActiveRemaining = strconv.FormatInt(remaining, 10)
			before, _ := json.Marshal(map[string]any{"target_id": entry.ID,
				"target_type": "receive_address", "status": entry.PrevStatus})
			after, _ := json.Marshal(map[string]any{"target_id": entry.ID,
				"target_type": "receive_address", "status": entry.Status,
				"decision_note": in.Reason})
			return addressID, entry, before, after, nil
		})
	if err != nil {
		return ManagedReceiveAddressStatus{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedReceiveAddressStatus{}, httpx.Internal
	}
	return out, nil
}

// AddSharedPayAddress 登记一个公共付款地址（交易所热钱包等）。
//
// 登记之后，从这个地址付款的订单不会被邀请风控当成自邀请的铁证，而是转人工复核
// （见 internal/invite/risk_rewards.go 的 SHARED_PAY_ADDRESS 标记）。
func (s *FinanceAddressService) AddSharedPayAddress(ctx context.Context, p ApprovalProof,
	in SharedPayAddressInput) (out ManagedSharedPayAddress, err error) {
	if s == nil || s.Security == nil {
		return out, httpx.Internal
	}
	if !nodeText(in.Address, 64, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "地址必须是 1–64 个可见字符")
	}
	if in.Note != "" && !nodeText(in.Note, 128, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "备注必须是 1–128 个可见字符")
	}

	raw, err := securedMutation(ctx, s.Security, "admin_finance_mutations", "admin-finance-write",
		p, actionSharedPay, "shared_pay_address", 1, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			// 把自己的收款地址标成「公共付款地址」是说不通的：它是收钱的一端，
			// 不是别人付款的来源。更要紧的是这么标之后，凡是查到这个地址的风控判断都会走偏。
			var own bool
			if e := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM receive_addresses WHERE address=$1)`,
				in.Address).Scan(&own); e != nil {
				return 0, nil, nil, nil, e
			}
			if own {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrBadRequest,
					"该地址是本系统的收款地址，不能登记为公共付款地址")
			}
			var note *string
			if in.Note != "" {
				note = &in.Note
			}
			var entry ManagedSharedPayAddress
			var id int64
			// auto_detected 恒为 false：本接口写入的是人工登记，与扫描自动识别的必须分得开。
			e := tx.QueryRow(c, `INSERT INTO shared_pay_addresses(address,note,auto_detected,created_at)
 VALUES($1,$2,false,$3) RETURNING address,note,auto_detected,created_at,id`,
				in.Address, note, now).
				Scan(&entry.Address, &entry.Note, &entry.AutoDetected, &entry.CreatedAt, &id)
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "23505" {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "该地址已登记")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			after, _ := json.Marshal(map[string]any{"target_type": "shared_pay_address",
				"target_id": strconv.FormatInt(id, 10), "decision_note": in.Reason})
			return id, entry, nil, after, nil
		})
	if err != nil {
		return ManagedSharedPayAddress{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedSharedPayAddress{}, httpx.Internal
	}
	return out, nil
}

// SharedPayAddresses 列出公共付款地址，沿用资金只读能力 finance.read。
func (s *FinanceReadService) SharedPayAddresses(ctx context.Context, p Principal, f FinanceFilter) (
	out ManagedSharedPayAddressPage, err error) {
	page, size, err := normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*) FROM shared_pay_addresses`).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT address,note,auto_detected,created_at
 FROM shared_pay_addresses ORDER BY created_at DESC,address LIMIT $1 OFFSET $2`,
			size, (page-1)*size)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedSharedPayAddressPage{Items: []ManagedSharedPayAddress{},
			Total: strconv.FormatInt(total, 10), Page: page, PageSize: size, ObservedAt: now}
		for rows.Next() {
			var v ManagedSharedPayAddress
			if e = rows.Scan(&v.Address, &v.Note, &v.AutoDetected, &v.CreatedAt); e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedSharedPayAddressPage{}, err
	}
	return out, nil
}
