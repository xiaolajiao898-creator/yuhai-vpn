package invite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const bindWindow = 72 * time.Hour
const rewardDescription = "邀请关系已绑定。奖励以实际启用的活动规则、审核结果和发放流水为准，绑定成功不代表已获得奖励。"

type BindInput struct {
	Code   string `json:"code"`
	Method string `json:"method"`
}

type BindResult struct {
	Bound             bool   `json:"bound"`
	RewardDescription string `json:"reward_description"`
}

type Binder struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

func normalizeBinding(in BindInput, registration bool) (BindInput, error) {
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	if len(in.Code) != 6 || strings.Trim(in.Code, "23456789ABCDEFGHJKMNPQRSTUVWXYZ") != "" {
		return in, httpx.NewError(httpx.ErrBadRequest, "邀请码须为 6 位字母或数字")
	}
	if in.Method != "MANUAL" && in.Method != "CLIPBOARD" && !(registration && in.Method == "APK_CHANNEL") {
		return in, httpx.NewError(httpx.ErrBadRequest, "邀请码来源无效")
	}
	return in, nil
}

// Bind commits the immutable relation and the idempotent success response in
// one transaction. Failed attempts create neither a relation nor a success key.
func (s *Binder) Bind(ctx context.Context, userID int64, in BindInput, idemKey string) (BindResult, error) {
	var result BindResult
	if userID <= 0 {
		return result, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份")
	}
	var err error
	if in, err = normalizeBinding(in, false); err != nil {
		return result, err
	}
	idem, err := uuid.Parse(idemKey)
	if err != nil {
		return result, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID")
	}
	idemKey = idem.String()
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		// api_idempotency has a cross-endpoint key namespace. Never overwrite a
		// different operation's stored response under the same user and UUID.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "api-idem:"+strconv.FormatInt(userID, 10)+":"+idemKey); err != nil {
			return err
		}
		var endpoint, previousHash string
		var response []byte
		err := tx.QueryRow(ctx, `SELECT endpoint,request_hash,response FROM api_idempotency WHERE user_id=$1 AND idem_key=$2`, userID, idemKey).Scan(&endpoint, &previousHash, &response)
		if err == nil {
			if endpoint != "invite.bind" || previousHash != hash {
				return httpx.NewError(httpx.ErrConflict, "幂等键冲突")
			}
			if err := json.Unmarshal(response, &result); err != nil || !result.Bound {
				return httpx.NewError(httpx.ErrConflict, "绑定记录已失效，请刷新邀请页面核实")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result, err = BindInTx(ctx, tx, userID, in, s.Clock)
		if err != nil {
			return err
		}
		response, err = json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO api_idempotency(user_id,idem_key,endpoint,request_hash,status_code,response) VALUES($1,$2,'invite.bind',$3,200,$4)`, userID, idemKey, hash, response)
		return err
	})
	return result, err
}

// BindInTx is shared by registration and later manual binding. It does no
// accounting and enables no reward rule. The caller owns commit/rollback.
//
// Lock order: relation-graph advisory -> invitee/payment advisory -> user rows
// in ID order. Payment paths share invite-payment:{userID} before their paid
// transition; this function never locks order/quote/balance rows.
func BindInTx(ctx context.Context, tx pgx.Tx, userID int64, in BindInput, c clock.Clock) (BindResult, error) {
	var result BindResult
	var err error
	if in, err = normalizeBinding(in, true); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('invite-relations',0))`); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "invite-payment:"+strconv.FormatInt(userID, 10)); err != nil {
		return result, err
	}
	var inviterID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE invite_code=$1`, in.Code).Scan(&inviterID); errors.Is(err, pgx.ErrNoRows) {
		return result, httpx.NewError(httpx.ErrInviteNotFound, "邀请码不存在")
	} else if err != nil {
		return result, err
	}
	if inviterID == userID {
		return result, httpx.NewError(httpx.ErrSelfInvite, "不能绑定自己的邀请码")
	}
	type participant struct {
		status, account, prefix, code string
		created                       time.Time
	}
	participants := map[int64]participant{}
	rows, err := tx.Query(ctx, `SELECT id,status,account_no,COALESCE(register_ip_prefix_hash,''),invite_code,created_at FROM users WHERE id IN ($1,$2) ORDER BY id FOR NO KEY UPDATE`, userID, inviterID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id int64
		var p participant
		if err := rows.Scan(&id, &p.status, &p.account, &p.prefix, &p.code, &p.created); err != nil {
			rows.Close()
			return result, err
		}
		participants[id] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(participants) != 2 {
		return result, httpx.NewError(httpx.ErrTokenMissing, "账户身份无效")
	}
	invitee, inviter := participants[userID], participants[inviterID]
	if invitee.status != "ACTIVE" || inviter.status != "ACTIVE" {
		return result, blockedBinding(ctx, userID, "ACCOUNT_INACTIVE")
	}
	var bound, reciprocal, paid bool
	err = tx.QueryRow(ctx, `SELECT
	 EXISTS(SELECT 1 FROM invite_relations WHERE invitee_user_id=$1),
	 EXISTS(SELECT 1 FROM invite_relations WHERE invitee_user_id=$2 AND inviter_user_id=$1),
	 EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND (paid_at IS NOT NULL OR status IN ('PAID','COMPLETED')))`, userID, inviterID).Scan(&bound, &reciprocal, &paid)
	if err != nil {
		return result, err
	}
	if bound {
		return result, httpx.NewError(httpx.ErrInviteBound, "已经绑定过邀请人")
	}
	now := c.Now() // Read after locks: a queued request cannot use an old timestamp.
	if paid || now.Before(invitee.created) || !now.Before(invitee.created.Add(bindWindow)) {
		return result, httpx.NewError(httpx.ErrInviteWindow, "已超过绑定时间或已完成首次付费")
	}
	if reciprocal {
		return result, blockedBinding(ctx, userID, "RECIPROCAL_INVITATION")
	}
	var blacklisted bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blacklist b WHERE b.status='ACTIVE'
	 AND (b.expires_at IS NULL OR b.expires_at>$7)
	 AND ((b.target_type='USER' AND b.target_value IN ($1,$2,$3,$4))
	 OR (b.target_type='INVITE_CODE' AND b.target_value IN ($5,$6))
	 OR (b.target_type IN ('DEVICE','FINGERPRINT','IP_PREFIX') AND
	      b.effects && ARRAY['INVITE_BIND','INVITE_REWARD','ALL']::text[] AND
	      ((b.target_type='IP_PREFIX' AND b.target_value IN (SELECT register_ip_prefix_hash FROM users WHERE id IN ($8,$9)))
	       OR EXISTS(SELECT 1 FROM devices d WHERE d.user_id IN ($8,$9) AND
	         ((b.target_type='DEVICE' AND b.target_value IN (d.id::text,d.device_uid::text))
	          OR (b.target_type='FINGERPRINT' AND b.target_value=d.fingerprint_hash)))))))`,
		strconv.FormatInt(userID, 10), strconv.FormatInt(inviterID, 10), invitee.account, inviter.account,
		in.Code, invitee.code, now, userID, inviterID).Scan(&blacklisted)
	if err != nil {
		return result, err
	}
	if blacklisted {
		return result, blockedBinding(ctx, userID, "BLACKLIST")
	}
	var sameDevice bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices a JOIN devices b ON b.user_id=$2
	 WHERE a.user_id=$1 AND ((a.fingerprint_hash IS NOT NULL AND a.fingerprint_hash<>'' AND a.fingerprint_hash=b.fingerprint_hash)
	 OR a.pubkey=b.pubkey))`, userID, inviterID).Scan(&sameDevice)
	if err != nil {
		return result, err
	}
	if sameDevice {
		return result, blockedBinding(ctx, userID, "SAME_DEVICE")
	}
	flags := []string{}
	delta := invitee.created.Sub(inviter.created)
	if delta < 0 {
		delta = -delta
	}
	if invitee.prefix != "" && invitee.prefix == inviter.prefix && delta < time.Hour {
		flags = append(flags, "REGISTER_IP_PROXIMITY")
	}
	riskJSON, _ := json.Marshal(flags)
	_, err = tx.Exec(ctx, `INSERT INTO invite_relations(invitee_user_id,inviter_user_id,invite_code,bind_method,risk_flags,created_at) VALUES($1,$2,$3,$4,$5,$6)`, userID, inviterID, in.Code, in.Method, riskJSON, now)
	if err != nil {
		return result, err
	}
	return BindResult{Bound: true, RewardDescription: rewardDescription}, nil
}

func blockedBinding(ctx context.Context, userID int64, reason string) error {
	slog.WarnContext(ctx, "invite binding denied", "user_id", userID, "reason", reason)
	return httpx.NewError(httpx.ErrRiskBlocked, "暂时无法绑定")
}
