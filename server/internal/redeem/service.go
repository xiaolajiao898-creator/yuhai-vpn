package redeem

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
)

var (
	ErrBadFormat = httpx.NewError(httpx.ErrBadRequest, "兑换码格式不正确")
	ErrNotFound  = httpx.NewError(httpx.ErrNotFound, "码不存在")
	ErrUsed      = httpx.NewError(httpx.ErrConflict, "已使用")
	ErrRevoked   = httpx.NewError(httpx.ErrConflict, "已作废")
)

type Service struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

type Result struct {
	CardType     string    `json:"card_type"`
	CardName     string    `json:"card_name"`
	DurationDays int       `json:"duration_days"`
	Tier         int       `json:"tier"`
	DeviceLimit  int       `json:"device_limit"`
	Unlimited    bool      `json:"unlimited_traffic"`
	ExpireAt     time.Time `json:"expire_at"`
	// Extended 为 true 表示原来就有未过期权益，天数加在原到期时间之后。
	Extended bool `json:"extended"`
}

// Redeem 兑换一张码。同一张码的并发请求会串行，只有第一次写入权益。
// 叠加规则见 entitlement.ApplyRedeem。
func (s *Service) Redeem(ctx context.Context, userID int64, raw string) (Result, error) {
	var none Result
	norm, err := Normalize(raw)
	if err != nil {
		return none, ErrBadFormat
	}
	if s == nil || s.Pool == nil || s.Clock == nil || userID <= 0 {
		return none, httpx.Internal
	}
	hash := Hash(norm)
	var out Result
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `SELECT id FROM redeem_codes WHERE code_hash = $1`, hash).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 顾问锁不需要整表 UPDATE 权限。兑换和作废都锁同一行，避免一边开通一边作废。
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "redeem-row:"+itoa(id)); err != nil {
			return err
		}
		var status, cardType, cardName string
		var days, tier, devices int
		var traffic *int64
		err = tx.QueryRow(ctx, `
			SELECT c.status, c.card_type, t.name, c.duration_days, c.tier, c.device_limit, c.traffic_bytes
			  FROM redeem_codes c
			  JOIN redeem_card_types t ON t.code = c.card_type
			 WHERE c.id = $1`, id).Scan(&status, &cardType, &cardName, &days, &tier, &devices, &traffic)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch status {
		case "used":
			return ErrUsed
		case "revoked":
			return ErrRevoked
		case "unused":
		default:
			return httpx.Internal
		}
		now := s.Clock.Now()
		applied, err := entitlement.ApplyRedeem(ctx, tx, entitlement.RedeemGrant{
			UserID: userID, CodeID: id, DurationDays: days, Tier: tier, DeviceLimit: devices,
			QuotaBytes: traffic, Remark: "兑换" + cardName,
		}, now)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE redeem_codes
			   SET status = 'used', redeemed_by = $2, redeemed_at = $3
			 WHERE id = $1 AND status = 'unused'`, id, userID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrUsed
		}
		out = Result{
			CardType: cardType, CardName: cardName, DurationDays: days,
			Tier: applied.Tier, DeviceLimit: applied.DeviceLimit, Unlimited: applied.Unlimited,
			ExpireAt: applied.ExpireAt, Extended: applied.Extended,
		}
		return nil
	})
	if err != nil {
		return none, err
	}
	return out, nil
}

// BatchInput 是生成一批码的参数。期限从卡类型表读取，不由调用方传入。
type BatchInput struct {
	CardType string
	Quantity int
	Note     string
	AdminID  int64
	Now      time.Time
}

// Batch 是刚生成的一批。Codes 只存在于这次返回值里，函数本身不把明文写进文本列。
type Batch struct {
	ID        int64
	CardType  string
	CardName  string
	Days      int
	Tier      int
	Devices   int
	Traffic   *int64
	Note      string
	Quantity  int
	Codes     []string
	CreatedAt time.Time
}

// CreateBatch 在调用方事务里写入批次和哈希。seal 必须把明文封存后写入 codes_enc。
func CreateBatch(ctx context.Context, tx pgx.Tx, in BatchInput, seal func(batchID int64, codes []string) ([]byte, error)) (Batch, error) {
	var none Batch
	if in.Quantity < 1 || in.Quantity > 500 {
		return none, httpx.NewError(httpx.ErrBadRequest, "数量必须在 1 到 500 之间")
	}
	if in.AdminID <= 0 || seal == nil {
		return none, httpx.Internal
	}
	card := strings.ToUpper(strings.TrimSpace(in.CardType))
	if card == "" {
		return none, httpx.NewError(httpx.ErrBadRequest, "卡类型不存在")
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > 200 {
		return none, httpx.NewError(httpx.ErrBadRequest, "备注过长")
	}
	var name string
	var days, tier, devices int
	var traffic *int64
	var enabled bool
	err := tx.QueryRow(ctx, `
		SELECT name, duration_days, tier, device_limit, traffic_bytes, enabled
		  FROM redeem_card_types WHERE code = $1`, card).Scan(&name, &days, &tier, &devices, &traffic, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, httpx.NewError(httpx.ErrBadRequest, "卡类型不存在")
	}
	if err != nil {
		return none, err
	}
	if !enabled {
		return none, httpx.NewError(httpx.ErrBadRequest, "该卡类型已停用")
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var batchID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO redeem_batches(card_type, quantity, note, codes_enc, created_by, created_at)
		VALUES ($1, $2, NULLIF($3, ''), decode('00', 'hex'), $4, $5)
		RETURNING id`, card, in.Quantity, note, in.AdminID, now).Scan(&batchID)
	if err != nil {
		return none, err
	}
	codes := make([]string, 0, in.Quantity)
	seen := make(map[string]struct{}, in.Quantity)
	attempts := 0
	for len(codes) < in.Quantity {
		attempts++
		if attempts > in.Quantity+32 {
			return none, errors.New("redeem code space exhausted")
		}
		formatted, err := GenerateCode()
		if err != nil {
			return none, err
		}
		norm, err := Normalize(formatted)
		if err != nil {
			return none, err
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO redeem_codes(code_hash, code_hint, batch_id, card_type, duration_days, tier, device_limit, traffic_bytes, note, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10, $11)
			ON CONFLICT (code_hash) DO NOTHING`,
			Hash(norm), Hint(norm), batchID, card, days, tier, devices, traffic, note, in.AdminID, now)
		if err != nil {
			return none, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		seen[norm] = struct{}{}
		codes = append(codes, Format(norm))
	}
	blob, err := seal(batchID, codes)
	if err != nil {
		return none, err
	}
	if len(blob) == 0 || bytesContainsAny(blob, codes) {
		return none, errors.New("refusing to store redeem plaintext")
	}
	if _, err = tx.Exec(ctx, `UPDATE redeem_batches SET codes_enc = $2 WHERE id = $1`, batchID, blob); err != nil {
		return none, err
	}
	return Batch{
		ID: batchID, CardType: card, CardName: name, Days: days, Tier: tier, Devices: devices,
		Traffic: traffic, Note: note, Quantity: len(codes), Codes: codes, CreatedAt: now,
	}, nil
}

func bytesContainsAny(blob []byte, codes []string) bool {
	raw := string(blob)
	for _, code := range codes {
		if strings.Contains(raw, code) || strings.Contains(raw, strings.ReplaceAll(code, "-", "")) {
			return true
		}
	}
	return false
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
