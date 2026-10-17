// Package balance 实现余额入账/出账原语：快照表 user_balances 乐观锁 + 只追加流水 balance_ledger（方案 10.3、12.3）。
// 每次变动必须带业务确定性幂等键；重复键直接返回已有结果，不报错。
package balance

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/money"
)

var ErrInsufficient = errors.New("insufficient balance")
var ErrOverflow = errors.New("balance exceeds int64 range")
var ErrIdempotencyConflict = errors.New("balance idempotency identity conflict")

type Entry struct {
	UserID     int64
	EntryType  string      // DEPOSIT / PURCHASE / INVITE_REWARD / EXCEPTION_CREDIT / FULFILL_FALLBACK / ADMIN_CREDIT / ADMIN_DEBIT
	Amount     money.Micro // 正数入账，负数出账
	SourceType string      // CHAIN_TRANSFER / ORDER / INVITE_REWARD / DEPOSIT_EXCEPTION / ADMIN_APPROVAL
	SourceID   int64
	IdemKey    string
	Operator   string // USER / ADMIN / SYSTEM
	OperatorID *int64
	Remark     string
}

// Apply 在调用方事务内执行一次余额变动。返回变动后的余额；幂等重放时返回当时记录的 balance_after。
func Apply(ctx context.Context, tx pgx.Tx, e Entry) (money.Micro, error) {
	if e.Amount == 0 || e.UserID <= 0 || e.SourceID <= 0 || e.IdemKey == "" {
		return 0, errors.New("invalid balance entry")
	}
	// Serialize the same operation before inspecting its ledger identity. This
	// also closes the race between a missing-key lookup and the balance row lock.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "balance:"+e.IdemKey); err != nil {
		return 0, err
	}
	var after int64
	var userID, amount, sourceID int64
	var entryType, sourceType string
	err := tx.QueryRow(ctx, `SELECT balance_after_micro,user_id,amount_micro,source_id,entry_type,source_type FROM balance_ledger WHERE idempotency_key = $1`, e.IdemKey).Scan(&after, &userID, &amount, &sourceID, &entryType, &sourceType)
	if err == nil {
		if userID != e.UserID || amount != int64(e.Amount) || sourceID != e.SourceID || entryType != e.EntryType || sourceType != e.SourceType {
			return 0, ErrIdempotencyConflict
		}
		return money.Micro(after), nil // 已执行过
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	// 锁住余额行（注册时已初始化；兜底再插一次）
	if _, err := tx.Exec(ctx, `INSERT INTO user_balances (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, e.UserID); err != nil {
		return 0, err
	}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT available_micro FROM user_balances WHERE user_id = $1 FOR UPDATE`, e.UserID).Scan(&current); err != nil {
		return 0, err
	}
	if e.Amount > 0 && current > math.MaxInt64-int64(e.Amount) {
		return money.Micro(current), ErrOverflow
	}
	if e.Amount < 0 && int64(e.Amount) < -current {
		return money.Micro(current), ErrInsufficient
	}
	next := current + int64(e.Amount)
	if _, err := tx.Exec(ctx, `UPDATE user_balances SET available_micro = $2, version = version + 1, updated_at = now() WHERE user_id = $1`, e.UserID, next); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO balance_ledger (user_id, entry_type, amount_micro, balance_after_micro, source_type, source_id, idempotency_key, operator_type, operator_id, remark)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''))`,
		e.UserID, e.EntryType, int64(e.Amount), next, e.SourceType, e.SourceID, e.IdemKey, e.Operator, e.OperatorID, e.Remark)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, fmt.Errorf("balance ledger idempotency race: %w", err)
		}
		return 0, err
	}
	return money.Micro(next), nil
}
