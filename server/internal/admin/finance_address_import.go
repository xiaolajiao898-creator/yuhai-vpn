package admin

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 收款地址导入（admin.yaml POST /receive-addresses/import）。
//
// 在这之前，往 receive_addresses 里加地址的唯一办法是把 `cmd/fundsign sign` 打出来的
// INSERT 语句贴进 psql 执行——也就是说**谁有库的写权限，谁就能往收款地址表里塞任何地址**，
// 签名字段爱填什么填什么。而这张表决定用户往哪儿付钱。
//
// 这一节把那条路收进接口，并且第一次在服务端真正验签：
//
//   - 签名原文格式与 `cmd/fundsign` 一致：`yuhai-addr-v1|{network}|{address}|{valid_until_unix}`。
//     此前全项目**没有任何地方验证过它**——公钥配在 FUND_ADDRESS_PUBKEY_B64 里，
//     但只有客户端在核对，服务端照单全收。
//   - 验不过的条目**整包拒绝**，一条都不入库。半包导入会留下一批来路不明的地址，
//     而运维看到的只是「导入成功」。
//   - 导入的地址一律是 PAUSED，**不是 ACTIVE**：刚进来的收款地址不该在没人看过之前
//     就开始收钱。要启用走第 24 节的 POST /receive-addresses/{id}/status。
//     （与「新建节点恒为 MAINTENANCE、新建入站默认 DISABLED」同一条规矩。）
//
// 这是 L3 双人审批动作：发起时只建审批单并把整包原样存进 payload，
// 真正入库发生在另一名管理员点通过时（执行器 executeAddressImport）。
// 发起时也验一遍签名——不合格的包没必要占着审批队列。

const (
	actionAddressImport = "finance.address_import"
	addressImportTarget = "receive_address_batch"
	// 签名原文前缀，与 cmd/fundsign 保持一致。改动需要同时改那边。
	addressSignPrefix = "yuhai-addr-v1"
	// 单包地址数量上限。资金系统一次签一批，几十个足够；
	// 设上限是因为整包会原样进审批 payload（jsonb）与审计。
	maxImportAddresses = 50
)

// AddressImportPackage 是资金系统离线签名后交过来的地址包。
type AddressImportPackage struct {
	KeyID     string               `json:"key_id"`
	Addresses []AddressImportEntry `json:"addresses"`
}

type AddressImportEntry struct {
	Network    string    `json:"network"`
	Address    string    `json:"address"`
	Label      string    `json:"label"`
	ValidUntil time.Time `json:"valid_until"`
	Signature  string    `json:"signature"`
}

type AddressImportInput struct {
	Reason  string               `json:"reason"`
	Package AddressImportPackage `json:"package"`
}

// FinanceImportService 发起地址包导入审批。
//
// FundPubKey 是 FUND_ADDRESS_PUBKEY_B64 的值（标准 Base64 的 Ed25519 公钥）。
// 没有配置时这个接口直接拒绝——宁可不能导入，也不要在没有验签能力的情况下放行。
type FinanceImportService struct {
	Security   *ApprovalService
	FundPubKey string
}

// RequestImport 发起一张地址包导入审批单。
func (s *FinanceImportService) RequestImport(ctx context.Context, p ApprovalProof,
	in AddressImportInput) (Approval, error) {
	if s == nil || s.Security == nil {
		return Approval{}, httpx.Internal
	}
	e := s.Security.auditEvent(p, actionAddressImport, addressImportTarget, 0)
	return s.Security.mutate(ctx, p, e, func(ctx context.Context, tx pgx.Tx, event *audit.Event) (Approval, error) {
		var out Approval
		actor, err := positiveApprovalID(p.Principal.ID)
		if err != nil {
			return out, errSession
		}
		if !approvalText(in.Reason) {
			return out, httpx.NewError(httpx.ErrBadRequest, "导入理由无效")
		}
		key, err := fundPublicKey(s.FundPubKey)
		if err != nil {
			return out, err
		}
		payload, hash, err := canonicalImportPackage(in.Package, key)
		if err != nil {
			return out, err
		}
		if err = lockApprovalUsers(ctx, tx, actor, actor); err != nil {
			return out, err
		}
		if err = s.Security.Auth.ConsumeStepUp(ctx, tx, p.Principal, p.Token, actionAddressImport,
			p.RequestHash, p.IP); err != nil {
			return out, err
		}
		event.StepUpVerified = true
		if err = lockApprovalPermission(ctx, tx, actor, actionAddressImport, 3,
			s.Security.Auth.Clock.Now()); err != nil {
			return out, err
		}
		// 同一个包重复发起：复用仍然有效的那张待审批单，不堆第二张。
		// 判定靠 payload_hash——同一批地址、同样的签名，就是同一件事。
		previous, err := scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+`
 FROM admin_approvals WHERE action_code=$1 AND target_type=$2 AND payload_hash=$3
 AND status='PENDING' ORDER BY id LIMIT 1 FOR UPDATE`,
			actionAddressImport, addressImportTarget, hash))
		switch {
		case err == nil && previous.ExpiresAt.After(s.Security.Auth.Clock.Now()):
			if previous.RequestedBy != p.Principal.ID {
				return out, httpx.NewError(httpx.ErrConflict, "该地址包已有其他管理员发起的待审批申请")
			}
			out = previous
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
 VALUES($1,$2,NULL,$3,$4,$5,$6,$7,$8) RETURNING `+approvalColumns,
				actionAddressImport, addressImportTarget, payload, hash, in.Reason, actor,
				now, now.Add(approvalTTL)))
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

// executeAddressImport 是 finance.address_import 的执行器：在审批通过的同一事务里入库。
//
// 这里**重新验一遍签名**，不是重复劳动：审批单可能在队列里放了一天，
// 而执行才是真正让这些地址进入收款流程的那一刻；验签的代价远小于「没验就入库」的后果。
func executeAddressImport(ctx context.Context, s *ApprovalService, tx pgx.Tx, p ApprovalProof,
	out Approval, approvalID int64, now time.Time) error {
	var pkg AddressImportPackage
	if len(out.payload) > 64*1024 || decodeApprovalObject(out.payload, &pkg) != nil {
		return httpx.NewError(httpx.ErrApprovalStale, "地址包已损坏，请重新发起")
	}
	key, err := fundPublicKey(s.FundPubKey)
	if err != nil {
		return err
	}
	payload, hash, err := canonicalImportPackage(pkg, key)
	if err != nil {
		return err
	}
	// 落库内容必须与审批时逐字节一致：payload_hash 对不上就是被改过。
	if hash != out.PayloadHash || len(payload) == 0 {
		return httpx.NewError(httpx.ErrApprovalStale, "地址包与审批时不一致，请重新发起")
	}
	if err = s.freshProof(ctx, tx, p); err != nil {
		return err
	}
	if !out.ExpiresAt.After(now) {
		return httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
	}
	for _, a := range pkg.Addresses {
		// 状态恒为 PAUSED：刚导入的地址不该在没人看过之前就开始收钱。
		_, err = tx.Exec(ctx, `INSERT INTO receive_addresses(network,address,label,signature,
 sig_key_id,valid_until,status,approval_id,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,'PAUSED',$7,$8,$8)`,
			a.Network, a.Address, nullableLabel(a.Label), a.Signature, pkg.KeyID,
			a.ValidUntil, approvalID, now)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			// 整包回滚：半包导入会留下一批来路不明的地址，而运维看到的只是「导入成功」。
			return httpx.NewError(httpx.ErrConflict, "地址 "+a.Address+" 已存在，整包未导入")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func nullableLabel(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// fundPublicKey 解析配置里的资金系统公钥。
func fundPublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		// 没配公钥就没有验签能力。宁可不能导入，也不要在这种状态下放行。
		return nil, httpx.NewError(httpx.ErrMaintenance,
			"未配置资金系统公钥（FUND_ADDRESS_PUBKEY_B64），无法校验地址包")
	}
	return ed25519.PublicKey(raw), nil
}

// canonicalImportPackage 校验整包并给出规范化后的 JSON 与其校验和。
//
// 规范化的意义在这里是实打实的：审批 payload 与 payload_hash 要在一天后重新对得上，
// 而请求里的空白、字段顺序、时间写法都可能不同。
func canonicalImportPackage(pkg AddressImportPackage, key ed25519.PublicKey) ([]byte, string, error) {
	bad := func(msg string) ([]byte, string, error) {
		return nil, "", httpx.NewError(httpx.ErrBadRequest, msg)
	}
	if !nodeText(pkg.KeyID, 32, false) {
		return bad("key_id 必须是 1–32 个可见字符")
	}
	if len(pkg.Addresses) == 0 || len(pkg.Addresses) > maxImportAddresses {
		return bad("地址包必须包含 1–" + strconv.Itoa(maxImportAddresses) + " 个地址")
	}
	seen := map[string]bool{}
	canonical := AddressImportPackage{KeyID: pkg.KeyID,
		Addresses: make([]AddressImportEntry, 0, len(pkg.Addresses))}
	for _, a := range pkg.Addresses {
		if a.Network != "TRON" && a.Network != "MOCK" {
			return bad("network 只能是 TRON 或 MOCK")
		}
		if !nodeText(a.Address, 64, false) {
			return bad("地址必须是 1–64 个可见字符")
		}
		if a.Label != "" && !nodeText(a.Label, 64, false) {
			return bad("标签必须是 1–64 个可见字符")
		}
		if seen[a.Address] {
			// 包内自己重复：与其让唯一约束在执行时才炸，不如现在就说清楚。
			return bad("地址 " + a.Address + " 在包内重复")
		}
		seen[a.Address] = true
		if a.ValidUntil.IsZero() {
			return bad("valid_until 缺失")
		}
		sig, err := base64.RawURLEncoding.DecodeString(a.Signature)
		if err != nil || len(sig) != ed25519.SignatureSize {
			return bad("地址 " + a.Address + " 的签名格式无效")
		}
		// 签名原文与 cmd/fundsign 完全一致；秒级时间戳意味着 valid_until 必须精确到秒。
		msg := fmt.Sprintf("%s|%s|%s|%d", addressSignPrefix, a.Network, a.Address,
			a.ValidUntil.Unix())
		if !ed25519.Verify(key, []byte(msg), sig) {
			// 不指出是哪一项对不上：验签失败就是验签失败，逐项解释只会帮到伪造的人。
			return bad("地址 " + a.Address + " 的签名校验失败，整包未导入")
		}
		canonical.Addresses = append(canonical.Addresses, AddressImportEntry{
			Network: a.Network, Address: a.Address, Label: a.Label,
			ValidUntil: a.ValidUntil.UTC().Truncate(time.Second), Signature: a.Signature})
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, "", httpx.Internal
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
