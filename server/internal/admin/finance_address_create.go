package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/tron"
)

// 登记收款地址是单人 L2：二次验证、审计，立刻写入。
// 地址只负责收 USDT。系统不保存、也不使用它的私钥。
const actionAddressCreate = "finance.address_create"

type CreateReceiveAddressInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	Network     string `json:"network"`
	Address     string `json:"address"`
	Label       string `json:"label"`
	ValidUntil  string `json:"valid_until"`
}

type ManagedReceiveAddressCreated struct {
	ID         string    `json:"id"`
	Network    string    `json:"network"`
	Address    string    `json:"address"`
	Label      *string   `json:"label"`
	Status     string    `json:"status"`
	ValidUntil time.Time `json:"valid_until"`
}

// CreateReceiveAddress 由一名管理员登记收款地址。新地址一律暂停，要再点一次启停才会接单。
func (s *FinanceAddressService) CreateReceiveAddress(ctx context.Context, p ApprovalProof, in CreateReceiveAddressInput) (out ManagedReceiveAddressCreated, err error) {
	in.Address = strings.TrimSpace(in.Address)
	in.Label = strings.TrimSpace(in.Label)
	in.Network = strings.TrimSpace(in.Network)
	if in.Network != "TRON" && in.Network != "MOCK" {
		return out, httpx.NewError(httpx.ErrBadRequest, "网络只能是 TRON 或 MOCK")
	}
	if !nodeText(in.Address, 64, false) || in.Address != strings.TrimSpace(in.Address) {
		return out, httpx.NewError(httpx.ErrBadRequest, "地址必须是 1–64 个可见字符")
	}
	if in.Label != "" && !nodeText(in.Label, 64, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "备注必须是 1–64 个可见字符")
	}
	switch in.Network {
	case "TRON":
		if !tron.ValidAddress(in.Address) {
			return out, httpx.NewError(httpx.ErrBadRequest, "不是有效的 TRON 地址")
		}
	case "MOCK":
		if tron.ValidAddress(in.Address) {
			return out, httpx.NewError(httpx.ErrBadRequest, "模拟地址不能是真实的 TRON 地址")
		}
	}
	if s == nil || s.Security == nil {
		return out, httpx.Internal
	}

	raw, err := securedMutation(ctx, s.Security, "admin_finance_mutations", "admin-finance-write",
		p, actionAddressCreate, "receive_address", 0, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			until := now.AddDate(10, 0, 0)
			if in.ValidUntil != "" {
				parsed, e := time.Parse(time.RFC3339, in.ValidUntil)
				if e != nil {
					return 0, nil, nil, nil, httpx.NewError(httpx.ErrBadRequest, "有效期必须是 RFC3339 时间")
				}
				if !parsed.After(now) || parsed.After(now.AddDate(20, 0, 0)) {
					return 0, nil, nil, nil, httpx.NewError(httpx.ErrBadRequest, "有效期必须晚于现在，且不超过 20 年")
				}
				until = parsed
			}
			var label *string
			if in.Label != "" {
				label = &in.Label
			}
			var entry ManagedReceiveAddressCreated
			var id int64
			// signature 与 sig_key_id 留空：没有资金密钥，也不接收私钥。
			e := tx.QueryRow(c, `INSERT INTO receive_addresses
 (network, address, label, signature, sig_key_id, valid_until, status, created_at, updated_at)
 VALUES ($1, $2, $3, NULL, NULL, $4, 'PAUSED', $5, $5)
 RETURNING id, id::text, network, address, label, status, valid_until`,
				in.Network, in.Address, label, until, now).
				Scan(&id, &entry.ID, &entry.Network, &entry.Address, &entry.Label, &entry.Status, &entry.ValidUntil)
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "23505" {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "该地址已登记")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			if entry.Status != "PAUSED" {
				return 0, nil, nil, nil, httpx.Internal
			}
			// 审计快照只接受白名单里的短字段。地址原文不进快照（见 audit.snapshotFields），
			// 这一行用 target_id 指回 receive_addresses。请求哈希已经绑住提交的地址。
			after, _ := json.Marshal(map[string]any{
				"target_id": entry.ID, "target_type": "receive_address",
				"status":        entry.Status,
				"decision_note": in.Reason,
			})
			return id, entry, nil, after, nil
		})
	if err != nil {
		return ManagedReceiveAddressCreated{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedReceiveAddressCreated{}, httpx.Internal
	}
	return out, nil
}
