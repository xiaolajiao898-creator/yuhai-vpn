package admin

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"strings"
	"testing"
	"time"
)

type authAuditFunc func(context.Context, pgx.Tx, audit.Event) error

func (f authAuditFunc) Append(c context.Context, tx pgx.Tx, e audit.Event) error { return f(c, tx, e) }
func authCount(t *testing.T, s *Service, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func authAuditRows(t *testing.T, s *Service) string {
	t.Helper()
	var raw string
	if err := s.Pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(l))::text,'[]') FROM admin_audit_logs l`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
func authWrongCode(secret string, now time.Time) string {
	for n := 0; n < 10; n++ {
		code := fmt.Sprintf("%06d", n)
		matches := false
		for d := -1; d <= 1; d++ {
			if currentCode(secret, now.Add(time.Duration(d)*30*time.Second)) == code {
				matches = true
			}
		}
		if !matches {
			return code
		}
	}
	panic("no invalid code")
}

func TestAuthAuditSuccessAndUnknownIdentityIntegration(t *testing.T) {
	svc, clk, p, secret, ip, action := stepUpFixture(t)
	ctx := context.Background()
	step := issueStepUp(t, svc, clk, p, secret, ip, action, RequestHash("POST", "/admin/v1/example", []byte(`{}`)))
	if err := svc.Logout(withAuthRequest(ctx, "auth_lifecycle", ip), p.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, p.SessionID); err != nil {
		t.Fatal("logout replay", err)
	}
	for _, action := range []string{auditLogin, auditMFA, auditStepUp, auditLogout} {
		if n := authCount(t, svc, `SELECT count(*) FROM admin_audit_logs WHERE action_code=$1 AND result='SUCCESS' AND admin_user_id=$2 AND target_id=$2`, action, p.ID); n != 1 {
			t.Fatalf("%s success count %d", action, n)
		}
	}
	if n := authCount(t, svc, `SELECT count(*) FROM admin_audit_logs WHERE step_up_verified`); n != 2 {
		t.Fatal("MFA proof flags", n)
	}
	if _, err := svc.Login(ctx, LoginInput{"missing-account", "sentinel-wrong-password"}, ip); err == nil {
		t.Fatal("unknown account accepted")
	}
	if n := authCount(t, svc, `SELECT count(*) FROM admin_audit_logs WHERE action_code='auth.login' AND result='DENIED' AND admin_user_id IS NULL AND target_id IS NULL`); n != 1 {
		t.Fatal("invented identity", n)
	}
	raw := authAuditRows(t, svc)
	for _, v := range []string{p.Username, secret, step.Token, "sentinel-wrong-password", "missing-account"} {
		if strings.Contains(raw, v) {
			t.Fatal("unsafe audit content")
		}
	}
	a := &audit.Service{Pool: svc.Pool, Clock: clk}
	var fromID, toID int64
	if err := svc.Pool.QueryRow(ctx, `SELECT min(id),max(id) FROM admin_audit_logs`).Scan(&fromID, &toID); err != nil {
		t.Fatal(err)
	}
	verified, err := a.Verify(ctx, fromID, toID)
	if err != nil || !verified.Valid || verified.Checked != 5 {
		t.Fatal("invalid chain", verified, err)
	}
}

type authAuditFixture struct {
	s                                      *Service
	c                                      *testClock
	name, password, secret, ip, permission string
	challenge                              LoginResult
	p                                      Principal
}

func authOperationFixture(t *testing.T, action string) *authAuditFixture {
	t.Helper()
	f := &authAuditFixture{}
	if action == auditStepUp || action == auditLogout {
		f.s, f.c, f.p, f.secret, f.ip, f.permission = stepUpFixture(t)
		return f
	}
	var pair string
	f.s, f.c, f.name, f.password, pair = integrationService(t)
	f.secret, f.ip, _ = strings.Cut(pair, "|")
	if action == auditMFA {
		var err error
		f.challenge, err = f.s.Login(context.Background(), LoginInput{f.name, f.password}, f.ip)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *authAuditFixture) run(t *testing.T, action string, bad bool) error {
	t.Helper()
	ctx := context.Background()
	code := currentCode(f.secret, f.c.Now())
	password := f.password
	if bad {
		code = authWrongCode(f.secret, f.c.Now())
		password = "wrong credential"
	}
	switch action {
	case auditLogin:
		out, err := f.s.Login(ctx, LoginInput{f.name, password}, f.ip)
		if err != nil && out.MFATicket != "" {
			t.Fatal("failed login returned ticket")
		}
		return err
	case auditMFA:
		out, err := f.s.MFA(ctx, MFAInput{f.challenge.MFATicket, code}, f.ip, "Bearer sentinel-agent")
		if err != nil && (out.Token != "" || out.CSRFToken != "") {
			t.Fatal("failed MFA returned token")
		}
		return err
	case auditStepUp:
		out, err := f.s.IssueStepUp(ctx, f.p, StepUpInput{f.permission, RequestHash("POST", "/admin/v1/example", nil), code}, f.ip)
		if err != nil && out.Token != "" {
			t.Fatal("failed step-up returned token")
		}
		return err
	case auditLogout:
		return f.s.Logout(ctx, f.p.SessionID)
	}
	panic("unknown fixture operation")
}
func TestAuthAuditFailureRollsBackSuccessIntegration(t *testing.T) {
	for _, action := range []string{auditLogin, auditMFA, auditStepUp, auditLogout} {
		t.Run(action, func(t *testing.T) {
			f := authOperationFixture(t, action)
			oldStep := authCount(t, f.s, `SELECT totp_last_step FROM admin_users LIMIT 1`)
			real := &audit.Service{Pool: f.s.Pool, Clock: f.c}
			f.s.Audit = authAuditFunc(func(c context.Context, tx pgx.Tx, e audit.Event) error {
				if err := real.Append(c, tx, e); err != nil {
					return err
				}
				if e.Result == "SUCCESS" {
					return errors.New("failure after append: password=never-log")
				}
				return nil
			})
			err := f.run(t, action, false)
			if err == nil || safeAuthError(err).Code.HTTPStatus() != 500 || strings.Contains(err.Error(), "never-log") {
				t.Fatal("unsafe audit failure", err)
			}
			query := map[string]string{auditLogin: `SELECT count(*) FROM admin_mfa_tickets`, auditMFA: `SELECT count(*) FROM admin_sessions`, auditStepUp: `SELECT count(*) FROM admin_step_up_tokens`, auditLogout: `SELECT count(*) FROM admin_sessions WHERE revoked_at IS NOT NULL`}[action]
			if n := authCount(t, f.s, query); n != 0 {
				t.Fatal("unaudited auth state committed", n)
			}
			if action == auditMFA && authCount(t, f.s, `SELECT count(*) FROM admin_mfa_tickets WHERE used_at IS NOT NULL`) != 0 {
				t.Fatal("ticket consumed")
			}
			if got := authCount(t, f.s, `SELECT totp_last_step FROM admin_users LIMIT 1`); got != oldStep {
				t.Fatal("TOTP advanced on rollback")
			}
			if n := authCount(t, f.s, `SELECT count(*) FROM admin_audit_logs WHERE action_code=$1 AND result='FAILED'`, action); n != 1 {
				t.Fatal("failure audit missing", n)
			}
			if strings.Contains(authAuditRows(t, f.s), "sentinel-agent") {
				t.Fatal("raw agent logged")
			}
		})
	}
}
func TestAuthAuditOutagePreservesFailureBudgetsIntegration(t *testing.T) {
	for _, action := range []string{auditLogin, auditMFA, auditStepUp} {
		t.Run(action, func(t *testing.T) {
			f := authOperationFixture(t, action)
			prefix, _ := ipPrefix(f.ip)
			q := `SELECT COALESCE((SELECT attempts FROM admin_login_limits WHERE ip_hash=$1),0)`
			baseline := authCount(t, f.s, q, tokenHash(prefix))
			f.s.Audit = authAuditFunc(func(context.Context, pgx.Tx, audit.Event) error { return errors.New("audit unavailable") })
			for i := 0; i < maxFailures; i++ {
				if err := f.run(t, action, true); err == nil {
					t.Fatal("bad credential succeeded")
				}
			}
			if n := authCount(t, f.s, `SELECT count(*) FROM admin_users WHERE status='LOCKED' AND failed_attempts=5`); n != 1 {
				t.Fatal("audit outage lost lock", n)
			}
			if n := authCount(t, f.s, q, tokenHash(prefix)); n != baseline+maxFailures {
				t.Fatal("audit outage lost IP budget", n)
			}
		})
	}
}
func TestAuthAuditWaitCannotReturnExpiredAuthorityIntegration(t *testing.T) {
	for _, action := range []string{auditLogin, auditMFA, auditStepUp} {
		t.Run(action, func(t *testing.T) {
			f := authOperationFixture(t, action)
			real := &audit.Service{Pool: f.s.Pool, Clock: f.c}
			f.s.Audit = authAuditFunc(func(c context.Context, tx pgx.Tx, e audit.Event) error {
				if err := real.Append(c, tx, e); err != nil {
					return err
				}
				if e.Result == "SUCCESS" {
					f.c.Add(sessionTTL)
				}
				return nil
			})
			if err := f.run(t, action, false); err == nil {
				t.Fatal("expired authority returned")
			}
			if n := authCount(t, f.s, `SELECT count(*) FROM admin_audit_logs WHERE action_code=$1 AND result='SUCCESS'`, action); n != 0 {
				t.Fatal("expired success committed", n)
			}
		})
	}
}
