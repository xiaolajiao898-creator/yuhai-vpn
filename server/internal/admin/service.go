package admin

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const (
	sessionTTL  = 8 * time.Hour
	sessionIdle = 30 * time.Minute
	ticketTTL   = 2 * time.Minute
	lockTTL     = 15 * time.Minute
	maxFailures = 5
)

var (
	errCredentials = httpx.NewError(httpx.ErrSecretInvalid, "账户或凭据无效，请检查后重试")
	errMFA         = httpx.NewError(httpx.ErrTOTPInvalid, "验证已失效或动态口令不正确，请重新登录")
	errSession     = httpx.NewError(httpx.ErrTokenMissing, "管理会话已失效，请重新登录")
	errLimited     = httpx.NewError(httpx.ErrRateLimited, "登录尝试过多，请稍后重试")
)

type Service struct {
	Pool          *pgxpool.Pool
	Clock         clock.Clock
	Enc           *fieldenc.Cipher
	Audit         ApprovalAuditor // nil uses the mandatory database auditor
	dummyHash     string
	passwordSlots chan struct{}
}

func New(pool *pgxpool.Pool, c clock.Clock, enc *fieldenc.Cipher) (*Service, error) {
	if pool == nil || c == nil || enc == nil {
		return nil, errors.New("admin service dependencies missing")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	dummy, err := HashPassword(token)
	if err != nil {
		return nil, err
	}
	return &Service{Pool: pool, Clock: c, Enc: enc, dummyHash: dummy, passwordSlots: make(chan struct{}, 2)}, nil
}

type LoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type LoginResult struct {
	MFATicket string `json:"mfa_ticket"`
}
type MFAInput struct {
	MFATicket string `json:"mfa_ticket"`
	TOTPCode  string `json:"totp_code"`
}
type SessionResult struct {
	Token     string `json:"-"`
	CSRFToken string `json:"csrf_token"`
}
type Principal struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Permissions []string `json:"permissions"`
	SessionID   int64    `json:"-"`
	CSRFToken   string   `json:"csrf_token"`
}

func (p Principal) HasPermission(code string) bool {
	for _, c := range p.Permissions {
		if c == code {
			return true
		}
	}
	return false
}

type adminUser struct {
	id                             int64
	username, passwordHash, status string
	secret                         []byte
	lastStep                       int64
	failures                       int
	lockedUntil                    *time.Time
}

func readUser(ctx context.Context, tx pgx.Tx, query string, arg any) (u adminUser, err error) {
	err = tx.QueryRow(ctx, query, arg).Scan(&u.id, &u.username, &u.passwordHash, &u.secret, &u.lastStep, &u.status, &u.failures, &u.lockedUntil)
	return
}

const userColumns = `id,username,password_hash,totp_secret_enc,totp_last_step,status,failed_attempts,locked_until`

func (u adminUser) blocked(now time.Time) bool {
	return u.status == "DISABLED" || (u.lockedUntil != nil && u.lockedUntil.After(now)) || (u.status == "LOCKED" && u.lockedUntil == nil)
}
func failed(ctx context.Context, tx pgx.Tx, u adminUser, now time.Time) error {
	count := u.failures + 1
	if count > maxFailures {
		count = maxFailures
	}
	status := "ACTIVE"
	var until *time.Time
	if count >= maxFailures {
		status = "LOCKED"
		t := now.Add(lockTTL)
		until = &t
	}
	_, err := tx.Exec(ctx, `UPDATE admin_users SET failed_attempts=$2,status=$3,locked_until=$4,updated_at=$5 WHERE id=$1`, u.id, count, status, until, now)
	return err
}
func resetExpiredLock(ctx context.Context, tx pgx.Tx, u *adminUser, now time.Time) error {
	if u.status != "LOCKED" || u.lockedUntil == nil || u.lockedUntil.After(now) {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE admin_users SET status='ACTIVE',failed_attempts=0,locked_until=NULL,updated_at=$2 WHERE id=$1`, u.id, now)
	if err == nil {
		u.status = "ACTIVE"
		u.failures = 0
		u.lockedUntil = nil
	}
	return err
}

// limitLogin is persistent and shared by all admin-api instances. Network
// prefixes keep IP rotation inside the same /24 (IPv6 /64) in one budget.
func (s *Service) limitLogin(ctx context.Context, ip string) error {
	prefix, err := ipPrefix(ip)
	if err != nil {
		return errCredentials
	}
	now := s.Clock.Now()
	var count int
	err = s.Pool.QueryRow(ctx, `INSERT INTO admin_login_limits(ip_hash,window_start,attempts,expires_at) VALUES($1,$2,1,$3)
 ON CONFLICT(ip_hash) DO UPDATE SET
 attempts=CASE WHEN admin_login_limits.expires_at<=$2 THEN 1 ELSE LEAST(admin_login_limits.attempts+1,31) END,
 window_start=CASE WHEN admin_login_limits.expires_at<=$2 THEN $2 ELSE admin_login_limits.window_start END,
 expires_at=CASE WHEN admin_login_limits.expires_at<=$2 THEN $3 ELSE admin_login_limits.expires_at END RETURNING attempts`, tokenHash(prefix), now, now.Add(lockTTL)).Scan(&count)
	if err != nil {
		return err
	}
	if count > 30 {
		return errLimited
	}
	return nil
}

func (s *Service) Login(ctx context.Context, in LoginInput, ip string) (result LoginResult, err error) {
	event := authEvent(ctx, auditLogin, ip)
	defer func() {
		if err != nil {
			result = LoginResult{}
			err = s.authFailure(ctx, event, err)
		}
	}()
	if len(in.Username) == 0 || len(in.Username) > 32 || len(in.Password) == 0 || len(in.Password) > 1024 || !utf8.ValidString(in.Username) || strings.TrimSpace(in.Username) != in.Username {
		return result, errCredentials
	}
	if err := s.limitLogin(ctx, ip); err != nil {
		return result, err
	}
	select {
	case s.passwordSlots <- struct{}{}:
		defer func() { <-s.passwordSlots }()
	default:
		return result, errLimited
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer rollbackAuth(tx)
	u, err := readUser(ctx, tx, `SELECT `+userColumns+` FROM admin_users WHERE username=$1 FOR UPDATE`, in.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		VerifyPassword(s.dummyHash, in.Password)
		return result, errCredentials
	}
	if err != nil {
		return result, err
	}
	event.TargetID = &u.id
	now := s.Clock.Now()
	if err = resetExpiredLock(ctx, tx, &u, now); err != nil {
		return result, err
	}
	correct := VerifyPassword(u.passwordHash, in.Password)
	if u.blocked(now) {
		return result, errCredentials
	}
	if !correct {
		if err = persistCredentialFailure(ctx, tx, u, now); err != nil {
			return result, err
		}
		return result, errCredentials
	}
	event.AdminUserID = &u.id
	now = s.Clock.Now()
	ticket, err := randomToken()
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_mfa_tickets(token_hash,admin_user_id,ip,expires_at,created_at) VALUES($1,$2,$3,$4,$5)`, tokenHash(ticket), u.id, ip, now.Add(ticketTTL), now)
	if err != nil {
		return result, err
	}
	if err = s.authSuccess(ctx, tx, event, "MFA_REQUIRED"); err != nil {
		return result, err
	}
	// Appending can wait on the audit chain. Never return an expired ticket.
	if !now.Add(ticketTTL).After(s.Clock.Now()) {
		return result, errMFA
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	result.MFATicket = ticket
	return result, nil
}

// MFA locks the admin then challenge row. Consuming the ticket, advancing the
// TOTP step and inserting exactly one session share one transaction; retries
// can never create another session for the same challenge or code.
func (s *Service) MFA(ctx context.Context, in MFAInput, ip, userAgent string) (result SessionResult, err error) {
	event := authEvent(ctx, auditMFA, ip)
	defer func() {
		if err != nil {
			result = SessionResult{}
			err = s.authFailure(ctx, event, err)
		}
	}()
	if !validToken(in.MFATicket) {
		return result, errMFA
	}
	if err := s.limitLogin(ctx, ip); err != nil {
		return result, err
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer rollbackAuth(tx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT admin_user_id FROM admin_mfa_tickets WHERE token_hash=$1`, tokenHash(in.MFATicket)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, errMFA
	}
	if err != nil {
		return result, err
	}
	u, err := readUser(ctx, tx, `SELECT `+userColumns+` FROM admin_users WHERE id=$1 FOR UPDATE`, id)
	if err != nil {
		return result, err
	}
	event.TargetID = &u.id
	var ticketIP string
	var expiry time.Time
	var used *time.Time
	err = tx.QueryRow(ctx, `SELECT host(ip),expires_at,used_at FROM admin_mfa_tickets WHERE token_hash=$1 FOR UPDATE`, tokenHash(in.MFATicket)).Scan(&ticketIP, &expiry, &used)
	if err != nil {
		return result, err
	}
	now := s.Clock.Now()
	if used != nil || !expiry.After(now) || u.blocked(now) || !sameNetwork(ticketIP, ip) {
		return result, errMFA
	}
	// A challenge created before an account lock must never become usable again.
	if u.status != "ACTIVE" {
		return result, errMFA
	}
	secret, err := s.Enc.Decrypt(u.secret, fieldenc.AAD("admin_users", "totp_secret_enc", u.id))
	if err != nil {
		return result, err
	}
	step, ok := verifyTOTP(string(secret), in.TOTPCode, now, u.lastStep)
	clear(secret)
	if !ok {
		if err = persistCredentialFailure(ctx, tx, u, now); err != nil {
			return result, err
		}
		return result, errMFA
	}
	event.AdminUserID = &u.id
	event.StepUpVerified = true
	token, err := randomToken()
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `UPDATE admin_mfa_tickets SET used_at=$2 WHERE token_hash=$1`, tokenHash(in.MFATicket), now)
	if err != nil {
		return result, err
	}
	_, err = tx.Exec(ctx, `UPDATE admin_users SET totp_last_step=$2,failed_attempts=0,status='ACTIVE',locked_until=NULL,last_login_at=$3,last_login_ip=$4,updated_at=$3 WHERE id=$1`, u.id, step, now, ip)
	if err != nil {
		return result, err
	}
	userAgent = truncate(userAgent, 255)
	_, err = tx.Exec(ctx, `INSERT INTO admin_sessions(admin_user_id,token_hash,ip,user_agent,last_active_at,expires_at,created_at) VALUES($1,$2,$3,$4,$5,$6,$5)`, u.id, tokenHash(token), ip, userAgent, now, now.Add(sessionTTL))
	if err != nil {
		return result, err
	}
	if err = s.authSuccess(ctx, tx, event, "AUTHENTICATED"); err != nil {
		return result, err
	}
	if !expiry.After(s.Clock.Now()) || !now.Add(sessionIdle).After(s.Clock.Now()) {
		return result, errMFA
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return SessionResult{Token: token, CSRFToken: csrfToken(token)}, nil
}

func truncate(s string, n int) string {
	r := []rune(strings.ToValidUTF8(s, ""))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func (s *Service) Authenticate(ctx context.Context, token, ip string) (Principal, error) {
	var p Principal
	if !validToken(token) {
		return p, errSession
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var loginIP, status string
	var expires, last time.Time
	var revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT s.id,u.id,u.username,u.status,host(s.ip),s.expires_at,s.last_active_at,s.revoked_at FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_user_id WHERE s.token_hash=$1 FOR UPDATE OF s`, tokenHash(token)).Scan(&p.SessionID, &id, &p.Username, &status, &loginIP, &expires, &last, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errSession
	}
	if err != nil {
		return p, err
	}
	now := s.Clock.Now()
	if revoked != nil || status != "ACTIVE" || !expires.After(now) || !last.Add(sessionIdle).After(now) || !sameNetwork(loginIP, ip) {
		if revoked == nil {
			if _, err = tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, p.SessionID, now); err != nil {
				return p, err
			}
			if err = tx.Commit(ctx); err != nil {
				return p, err
			}
		}
		return Principal{}, errSession
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT p.permission_code FROM admin_user_roles r JOIN admin_role_permissions p ON p.role_id=r.role_id WHERE r.admin_user_id=$1 ORDER BY p.permission_code`, id)
	if err != nil {
		return p, err
	}
	p.Permissions = []string{}
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			rows.Close()
			return p, err
		}
		p.Permissions = append(p.Permissions, code)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	_, err = tx.Exec(ctx, `UPDATE admin_sessions SET last_active_at=$2 WHERE id=$1`, p.SessionID, now)
	if err != nil {
		return p, err
	}
	if err = tx.Commit(ctx); err != nil {
		return p, err
	}
	p.ID = adminID(id)
	p.CSRFToken = csrfToken(token)
	return p, nil
}
func (s *Service) Logout(ctx context.Context, sessionID int64) (err error) {
	event := authEvent(ctx, auditLogout, "")
	defer func() {
		if err != nil {
			err = s.authFailure(ctx, event, err)
		}
	}()
	if sessionID <= 0 {
		return errSession
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollbackAuth(tx)
	var id int64
	var revoked *time.Time
	// Match MFA/step-up lock order: user before session. Audit foreign keys
	// must never introduce a user lock while already holding only a session.
	err = tx.QueryRow(ctx, `SELECT admin_user_id FROM admin_sessions WHERE id=$1`, sessionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSession
	}
	if err != nil {
		return err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM admin_users WHERE id=$1 FOR UPDATE`, id).Scan(&id); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT revoked_at FROM admin_sessions WHERE id=$1 AND admin_user_id=$2 FOR UPDATE`, sessionID, id).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSession
	}
	if err != nil {
		return err
	}
	event.AdminUserID, event.TargetID = &id, &id
	if revoked != nil {
		return nil // Idempotent internal replay creates no duplicate transition.
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, sessionID, s.Clock.Now()); err != nil {
		return err
	}
	if err = s.authSuccess(ctx, tx, event, "REVOKED"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
