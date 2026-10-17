package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const stepUpTTL = 5 * time.Minute

var stepUpAction = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
var stepUpHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var stepUpCode = regexp.MustCompile(`^[0-9]{6}$`)
var errStepUp = httpx.NewError(httpx.ErrStepUpInvalid, "二次验证已失效，请重新验证后再操作")

type StepUpInput struct {
	ActionCode  string `json:"action_code"`
	PayloadHash string `json:"payload_hash"`
	TOTPCode    string `json:"totp_code"`
}
type StepUpResult struct {
	Token     string    `json:"step_up_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestHash includes the concrete target. Hashing only a JSON body would let
// an empty-body DELETE token authorize a different target. The caller must use
// r.Method, r.URL.RequestURI(), and the original bounded request-body bytes.
func RequestHash(method, requestURI string, body []byte) string {
	h := sha256.New()
	h.Write([]byte("v1\n" + method + "\n" + requestURI + "\n"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// lockStepUpAuthority rechecks live authorization, not a cached middleware
// snapshot. Call before business locks. Consistent order is user, session,
// permission rows, token, then business rows. Revocations wait for this tx.
func (s *Service) lockStepUpAuthority(ctx context.Context, tx pgx.Tx, p Principal, action, ip string) (adminUser, error) {
	id, err := strconv.ParseInt(p.ID, 10, 64)
	if err != nil || id <= 0 || p.SessionID <= 0 {
		return adminUser{}, errStepUp
	}
	u, err := readUser(ctx, tx, `SELECT `+userColumns+` FROM admin_users WHERE id=$1 FOR UPDATE`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, errStepUp
	}
	if err != nil {
		return u, err
	}
	var loginIP string
	var expiry, last time.Time
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT host(ip),expires_at,last_active_at,revoked_at FROM admin_sessions WHERE id=$1 AND admin_user_id=$2 FOR UPDATE`, p.SessionID, id).Scan(&loginIP, &expiry, &last, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, errStepUp
	}
	if err != nil {
		return u, err
	}
	now := s.Clock.Now()
	if u.status != "ACTIVE" || u.blocked(now) || revoked != nil || !expiry.After(now) || !last.Add(sessionIdle).After(now) || !sameNetwork(loginIP, ip) {
		return u, errStepUp
	}
	var risk int
	err = tx.QueryRow(ctx, `SELECT p.risk_level FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id JOIN admin_permissions p ON p.code=rp.permission_code WHERE ur.admin_user_id=$1 AND p.code=$2 AND p.risk_level>=2 FOR SHARE OF ur,rp,p`, id, action).Scan(&risk)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, httpx.NewError(httpx.ErrForbidden, "无权执行此操作")
	}
	return u, err
}

func (s *Service) IssueStepUp(ctx context.Context, p Principal, in StepUpInput, ip string) (out StepUpResult, err error) {
	event := authEvent(ctx, auditStepUp, ip)
	defer func() {
		if err != nil {
			out = StepUpResult{}
			err = s.authFailure(ctx, event, err)
		}
	}()
	if !stepUpAction.MatchString(in.ActionCode) || !stepUpHash.MatchString(in.PayloadHash) || !stepUpCode.MatchString(in.TOTPCode) {
		return out, httpx.NewError(httpx.ErrBadRequest, "操作或验证参数格式错误")
	}
	if err := s.limitLogin(ctx, ip); err != nil {
		return out, err
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer rollbackAuth(tx)
	u, err := s.lockStepUpAuthority(ctx, tx, p, in.ActionCode, ip)
	if err != nil {
		return out, err
	}
	event.AdminUserID, event.TargetID = &u.id, &u.id
	if err := s.stepUpSessionFresh(ctx, tx, p); err != nil {
		return out, err
	}
	now := s.Clock.Now()
	secret, err := s.Enc.Decrypt(u.secret, fieldenc.AAD("admin_users", "totp_secret_enc", u.id))
	if err != nil {
		return out, err
	}
	step, ok := verifyTOTP(string(secret), in.TOTPCode, now, u.lastStep)
	clear(secret)
	if !ok {
		if err = persistCredentialFailure(ctx, tx, u, now); err != nil {
			return out, err
		}
		return out, errStepUp
	}
	event.StepUpVerified = true
	token, err := randomToken()
	if err != nil {
		return out, err
	}
	expiry := now.Add(stepUpTTL)
	_, err = tx.Exec(ctx, `INSERT INTO admin_step_up_tokens(token_hash,session_id,action_code,payload_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, tokenHash(token), p.SessionID, in.ActionCode, in.PayloadHash, expiry)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE admin_users SET totp_last_step=$2,failed_attempts=0,updated_at=$3 WHERE id=$1`, u.id, step, now)
	if err != nil {
		return out, err
	}
	if err = s.authSuccess(ctx, tx, event, "ISSUED"); err != nil {
		return out, err
	}
	// The audit lock may outlive session/token deadlines. These rows are
	// already locked; no new business locks are taken after the audit lock.
	if err = s.stepUpSessionFresh(ctx, tx, p); err != nil {
		return out, err
	}
	if !expiry.After(s.Clock.Now()) {
		return out, errStepUp
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return StepUpResult{Token: token, ExpiresAt: expiry}, nil
}

// ConsumeStepUp MUST share the transaction of the protected mutation and its
// audit row. A rollback preserves the token; after commit, it cannot be reused.
// action and expectedHash must come from the server's route and actual request,
// never from client-supplied headers or payload fields. This is authorization
// infrastructure; L3 mutations additionally require independent approval.
func (s *Service) ConsumeStepUp(ctx context.Context, tx pgx.Tx, p Principal, token, action, expectedHash, ip string) error {
	if token == "" {
		return httpx.NewError(httpx.ErrStepUpRequired, "请先完成二次验证")
	}
	if tx == nil || !validToken(token) || !stepUpAction.MatchString(action) || !stepUpHash.MatchString(expectedHash) {
		return errStepUp
	}
	if _, err := s.lockStepUpAuthority(ctx, tx, p, action, ip); err != nil {
		return err
	}
	var session int64
	var storedAction, storedHash string
	var expiry time.Time
	var used *time.Time
	err := tx.QueryRow(ctx, `SELECT session_id,action_code,payload_hash,expires_at,used_at FROM admin_step_up_tokens WHERE token_hash=$1 FOR UPDATE`, tokenHash(token)).Scan(&session, &storedAction, &storedHash, &expiry, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return errStepUp
	}
	if err != nil {
		return err
	}
	if err := s.stepUpSessionFresh(ctx, tx, p); err != nil {
		return err
	}
	now := s.Clock.Now()
	if session != p.SessionID || storedAction != action || storedHash != expectedHash || used != nil || !expiry.After(now) {
		return errStepUp
	}
	_, err = tx.Exec(ctx, `UPDATE admin_step_up_tokens SET used_at=$2 WHERE token_hash=$1`, tokenHash(token), now)
	return err
}

// Authority and token row locks can wait. Recheck session deadlines after all
// locks are acquired; a request queued before expiry must not extend validity.
func (s *Service) stepUpSessionFresh(ctx context.Context, tx pgx.Tx, p Principal) error {
	var expires, last time.Time
	var revoked *time.Time
	err := tx.QueryRow(ctx, `SELECT expires_at,last_active_at,revoked_at FROM admin_sessions WHERE id=$1 AND admin_user_id=$2`, p.SessionID, p.ID).Scan(&expires, &last, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errStepUp
	}
	if err != nil {
		return err
	}
	now := s.Clock.Now()
	if revoked != nil || !expires.After(now) || !last.Add(sessionIdle).After(now) {
		return errStepUp
	}
	return nil
}
