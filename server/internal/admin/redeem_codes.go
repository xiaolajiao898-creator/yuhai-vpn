package admin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redeem"
)

// 兑换码由后台生成、用户在 App 里兑换。明文只出现在生成和导出的响应里，
// 列表只给后四位。叠加规则见 entitlement.ApplyRedeem。

const (
	redeemRead     = "redeem_codes.read"
	actionGenerate = "redeem_codes.generate"
	actionExport   = "redeem_codes.export"
	actionRevoke   = "redeem_codes.revoke"
)

var cardTypePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,15}$`)

type RedeemReadService struct{ Auth *Service }

type RedeemWriteService struct {
	Security *ApprovalService
	Cipher   *fieldenc.Cipher
}

type CardTypeView struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DurationDays int    `json:"duration_days"`
	Tier         int    `json:"tier"`
	DeviceLimit  int    `json:"device_limit"`
	Unlimited    bool   `json:"unlimited_traffic"`
	Enabled      bool   `json:"enabled"`
}

type CodeItem struct {
	ID              string     `json:"id"`
	Hint            string     `json:"hint"`
	BatchID         string     `json:"batch_id"`
	CardType        string     `json:"card_type"`
	CardName        string     `json:"card_name"`
	DurationDays    int        `json:"duration_days"`
	Tier            int        `json:"tier"`
	DeviceLimit     int        `json:"device_limit"`
	Unlimited       bool       `json:"unlimited_traffic"`
	Note            string     `json:"note"`
	Status          string     `json:"status"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	RedeemedBy      *string    `json:"redeemed_by"`
	RedeemedAccount *string    `json:"redeemed_account_no"`
	RedeemedAt      *time.Time `json:"redeemed_at"`
}

type CodePage struct {
	Items      []CodeItem `json:"items"`
	Total      string     `json:"total"`
	Page       int        `json:"page"`
	PageSize   int        `json:"page_size"`
	ObservedAt time.Time  `json:"observed_at"`
}

type CodeFilter struct {
	Page, PageSize   int
	CardType, Status string
	BatchID, Note    string
}

type GenerateInput struct {
	OperationID string `json:"operation_id"`
	CardType    string `json:"card_type"`
	Quantity    int    `json:"quantity"`
	Note        string `json:"note"`
}

type ReasonInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

type batchReceipt struct {
	BatchID      string    `json:"batch_id"`
	CardType     string    `json:"card_type"`
	CardName     string    `json:"card_name"`
	Quantity     int       `json:"quantity"`
	DurationDays int       `json:"duration_days"`
	Tier         int       `json:"tier"`
	DeviceLimit  int       `json:"device_limit"`
	Unlimited    bool      `json:"unlimited_traffic"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

type BatchView struct {
	batchReceipt
	Codes []string `json:"codes"`
}

type ExportCode struct {
	Code   string `json:"code"`
	Status string `json:"status"`
}

type ExportView struct {
	BatchID  string       `json:"batch_id"`
	CardType string       `json:"card_type"`
	CardName string       `json:"card_name"`
	Note     string       `json:"note"`
	Codes    []ExportCode `json:"codes"`
}

type RevokeView struct {
	ID      string `json:"id"`
	BatchID string `json:"batch_id"`
	Status  string `json:"status"`
}

func (s *RedeemReadService) CardTypes(ctx context.Context, p Principal) ([]CardTypeView, error) {
	read := &FinanceReadService{Auth: s.Auth}
	var out []CardTypeView
	err := read.readWith(ctx, p, redeemRead, func(c context.Context, tx pgx.Tx, _ time.Time) error {
		rows, err := tx.Query(c, `SELECT code, name, duration_days, tier, device_limit, traffic_bytes IS NULL, enabled
			FROM redeem_card_types WHERE enabled ORDER BY duration_days, code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []CardTypeView{}
		for rows.Next() {
			var v CardTypeView
			if err := rows.Scan(&v.Code, &v.Name, &v.DurationDays, &v.Tier, &v.DeviceLimit, &v.Unlimited, &v.Enabled); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *RedeemReadService) List(ctx context.Context, p Principal, f CodeFilter) (CodePage, error) {
	var none CodePage
	if f.Status != "" && f.Status != "unused" && f.Status != "used" && f.Status != "revoked" {
		return none, httpx.NewError(httpx.ErrBadRequest, "状态无效")
	}
	if f.CardType != "" && !cardTypePattern.MatchString(f.CardType) {
		return none, httpx.NewError(httpx.ErrBadRequest, "卡类型无效")
	}
	var batchID int64
	if f.BatchID != "" {
		id, err := positiveApprovalID(f.BatchID)
		if err != nil {
			return none, httpx.NewError(httpx.ErrBadRequest, "批次编号无效")
		}
		batchID = id
	}
	note := strings.TrimSpace(f.Note)
	if utf8.RuneCountInString(note) > 200 || !cleanNote(note) {
		return none, httpx.NewError(httpx.ErrBadRequest, "备注无效")
	}
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 20
	}
	if f.PageSize > 50 {
		return none, httpx.NewError(httpx.ErrBadRequest, "每页最多50条")
	}
	read := &FinanceReadService{Auth: s.Auth}
	var page CodePage
	err := read.readWith(ctx, p, redeemRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		where := []string{"TRUE"}
		args := []any{}
		if f.Status != "" {
			args = append(args, f.Status)
			where = append(where, "c.status = $"+strconv.Itoa(len(args)))
		}
		if f.CardType != "" {
			args = append(args, f.CardType)
			where = append(where, "c.card_type = $"+strconv.Itoa(len(args)))
		}
		if batchID > 0 {
			args = append(args, batchID)
			where = append(where, "c.batch_id = $"+strconv.Itoa(len(args)))
		}
		if note != "" {
			args = append(args, "%"+escapeLike(note)+"%")
			where = append(where, "c.note ILIKE $"+strconv.Itoa(len(args))+" ESCAPE '\\'")
		}
		clause := strings.Join(where, " AND ")
		var total int64
		if err := tx.QueryRow(c, `SELECT count(*) FROM redeem_codes c WHERE `+clause, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
		rows, err := tx.Query(c, `SELECT c.id::text, c.code_hint, c.batch_id::text, c.card_type, t.name,
			c.duration_days, c.tier, c.device_limit, c.traffic_bytes IS NULL, COALESCE(c.note, ''), c.status,
			c.created_by::text, c.created_at, c.redeemed_by::text, u.account_no, c.redeemed_at
			FROM redeem_codes c
			JOIN redeem_card_types t ON t.code = c.card_type
			LEFT JOIN users u ON u.id = c.redeemed_by
			WHERE `+clause+` ORDER BY c.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []CodeItem{}
		for rows.Next() {
			var item CodeItem
			if err := rows.Scan(&item.ID, &item.Hint, &item.BatchID, &item.CardType, &item.CardName,
				&item.DurationDays, &item.Tier, &item.DeviceLimit, &item.Unlimited, &item.Note, &item.Status,
				&item.CreatedBy, &item.CreatedAt, &item.RedeemedBy, &item.RedeemedAccount, &item.RedeemedAt); err != nil {
				return err
			}
			item.Hint = "****-****-****-" + strings.TrimSpace(item.Hint)
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page = CodePage{Items: items, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		return nil
	})
	if err != nil {
		return none, err
	}
	return page, nil
}

func (s *RedeemWriteService) Generate(ctx context.Context, p ApprovalProof, in GenerateInput) (BatchView, error) {
	var none BatchView
	if s == nil || s.Cipher == nil {
		return none, httpx.Internal
	}
	note := strings.TrimSpace(in.Note)
	if in.Quantity < 1 || in.Quantity > 500 {
		return none, httpx.NewError(httpx.ErrBadRequest, "数量必须在 1 到 500 之间")
	}
	card := strings.ToUpper(strings.TrimSpace(in.CardType))
	if !cardTypePattern.MatchString(card) {
		return none, httpx.NewError(httpx.ErrBadRequest, "卡类型不存在")
	}
	if utf8.RuneCountInString(note) > 200 || !cleanNote(note) {
		return none, httpx.NewError(httpx.ErrBadRequest, "备注无效")
	}
	reason := note
	if reason == "" {
		reason = "生成兑换码"
	}
	raw, err := securedMutation(ctx, s.Security, "admin_redeem_mutations", "redeem", p, actionGenerate, "redeem_batch", 0, in.OperationID, reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			batch, err := redeem.CreateBatch(c, tx, redeem.BatchInput{
				CardType: card, Quantity: in.Quantity, Note: note, AdminID: actor, Now: now,
			}, func(id int64, codes []string) ([]byte, error) {
				return redeem.SealCodes(s.Cipher, id, codes)
			})
			if err != nil {
				return 0, nil, nil, nil, err
			}
			receipt := batchReceipt{
				BatchID: strconv.FormatInt(batch.ID, 10), CardType: batch.CardType, CardName: batch.CardName,
				Quantity: batch.Quantity, DurationDays: batch.Days, Tier: batch.Tier, DeviceLimit: batch.Devices,
				Unlimited: batch.Traffic == nil, Note: batch.Note, CreatedAt: batch.CreatedAt,
			}
			after := auditRedeem("redeem_codes.generate", batch.CardType, batch.CardName, receipt.BatchID, "unused", note)
			return batch.ID, receipt, nil, after, nil
		})
	if err != nil {
		return none, err
	}
	var view BatchView
	if err := json.Unmarshal(raw, &view); err != nil {
		return none, err
	}
	codes, err := s.openBatch(ctx, view.BatchID)
	if err != nil {
		return none, httpx.NewError(httpx.ErrConflict, "兑换码已写入，但明文暂时无法读出，请勿再次生成")
	}
	view.Codes = codes
	return view, nil
}

func (s *RedeemWriteService) Export(ctx context.Context, p ApprovalProof, batchID int64, in ReasonInput) (ExportView, error) {
	var none ExportView
	if s == nil || s.Cipher == nil || batchID <= 0 {
		return none, httpx.Internal
	}
	reason := strings.TrimSpace(in.Reason)
	if !approvalText(reason) {
		return none, httpx.NewError(httpx.ErrBadRequest, "请填写导出原因")
	}
	raw, err := securedMutation(ctx, s.Security, "admin_redeem_mutations", "redeem", p, actionExport, "redeem_batch", batchID, in.OperationID, reason,
		func(c context.Context, tx pgx.Tx, _ int64, _ time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var card, name, note string
			err := tx.QueryRow(c, `SELECT b.card_type, t.name, COALESCE(b.note, '')
				FROM redeem_batches b JOIN redeem_card_types t ON t.code = b.card_type WHERE b.id = $1`, batchID).Scan(&card, &name, &note)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "批次不存在")
			}
			if err != nil {
				return 0, nil, nil, nil, err
			}
			receipt := ExportView{BatchID: strconv.FormatInt(batchID, 10), CardType: card, CardName: name, Note: note, Codes: []ExportCode{}}
			after := auditRedeem(actionExport, card, name, receipt.BatchID, "unused", reason)
			return batchID, receipt, nil, after, nil
		})
	if err != nil {
		return none, err
	}
	var view ExportView
	if err := json.Unmarshal(raw, &view); err != nil {
		return none, err
	}
	codes, err := s.openBatch(ctx, view.BatchID)
	if err != nil {
		return none, httpx.NewError(httpx.ErrConflict, "这批兑换码的明文暂时无法读出")
	}
	status, err := s.codeStatus(ctx, batchID)
	if err != nil {
		return none, err
	}
	view.Codes = make([]ExportCode, 0, len(codes))
	for _, code := range codes {
		norm, err := redeem.Normalize(code)
		if err != nil {
			return none, httpx.NewError(httpx.ErrConflict, "这批兑换码的明文暂时无法读出")
		}
		st, ok := status[redeem.Hash(norm)]
		if !ok {
			return none, httpx.NewError(httpx.ErrConflict, "这批兑换码的明文暂时无法读出")
		}
		view.Codes = append(view.Codes, ExportCode{Code: code, Status: st})
	}
	return view, nil
}

func (s *RedeemWriteService) Revoke(ctx context.Context, p ApprovalProof, codeID int64, in ReasonInput) (RevokeView, error) {
	var none RevokeView
	if codeID <= 0 {
		return none, httpx.NewError(httpx.ErrBadRequest, "编号格式无效")
	}
	reason := strings.TrimSpace(in.Reason)
	if !approvalText(reason) {
		return none, httpx.NewError(httpx.ErrBadRequest, "请填写作废原因")
	}
	raw, err := securedMutation(ctx, s.Security, "admin_redeem_mutations", "redeem", p, actionRevoke, "redeem_code", codeID, in.OperationID, reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			if _, err := tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "redeem-row:"+strconv.FormatInt(codeID, 10)); err != nil {
				return 0, nil, nil, nil, err
			}
			var status, card, name, batchID string
			err := tx.QueryRow(c, `SELECT c.status, c.card_type, t.name, c.batch_id::text
				FROM redeem_codes c JOIN redeem_card_types t ON t.code = c.card_type WHERE c.id = $1`, codeID).
				Scan(&status, &card, &name, &batchID)
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "码不存在")
			}
			if err != nil {
				return 0, nil, nil, nil, err
			}
			idText := strconv.FormatInt(codeID, 10)
			switch status {
			case "used":
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "已使用")
			case "revoked":
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "已作废")
			case "unused":
			default:
				return 0, nil, nil, nil, httpx.Internal
			}
			tag, err := tx.Exec(c, `UPDATE redeem_codes SET status='revoked', revoked_by=$2, revoked_at=$3 WHERE id=$1 AND status='unused'`, codeID, actor, now)
			if err != nil {
				return 0, nil, nil, nil, err
			}
			if tag.RowsAffected() != 1 {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "已作废")
			}
			view := RevokeView{ID: idText, BatchID: batchID, Status: "revoked"}
			before := auditRedeem(actionRevoke, card, name, idText, "unused", "")
			after := auditRedeem(actionRevoke, card, name, idText, "revoked", reason)
			return codeID, view, before, after, nil
		})
	if err != nil {
		return none, err
	}
	var view RevokeView
	if err := json.Unmarshal(raw, &view); err != nil {
		return none, err
	}
	return view, nil
}

func (s *RedeemWriteService) openBatch(ctx context.Context, batchText string) ([]string, error) {
	id, err := strconv.ParseInt(batchText, 10, 64)
	if err != nil || id <= 0 {
		return nil, err
	}
	var blob []byte
	if err := s.Security.Auth.Pool.QueryRow(ctx, `SELECT codes_enc FROM redeem_batches WHERE id=$1`, id).Scan(&blob); err != nil {
		return nil, err
	}
	return redeem.OpenCodes(s.Cipher, id, blob)
}

func (s *RedeemWriteService) codeStatus(ctx context.Context, batchID int64) (map[string]string, error) {
	rows, err := s.Security.Auth.Pool.Query(ctx, `SELECT code_hash, status FROM redeem_codes WHERE batch_id=$1`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var hash, status string
		if err := rows.Scan(&hash, &status); err != nil {
			return nil, err
		}
		out[hash] = status
	}
	return out, rows.Err()
}

func auditRedeem(action, card, name, id, status, note string) json.RawMessage {
	m := map[string]string{"action_code": action, "code": card, "name": name, "target_id": id, "status": status}
	if note != "" {
		m["decision_note"] = note
	}
	b, _ := json.Marshal(m)
	return b
}

func cleanNote(s string) bool {
	if s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
