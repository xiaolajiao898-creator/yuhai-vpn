package admin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func stepUpFixture(t *testing.T) (*Service, *testClock, Principal, string, string, string) {
	t.Helper()
	svc, clk, name, password, fixture := integrationService(t)
	secret, ip, _ := strings.Cut(fixture, "|")
	ctx := context.Background()
	challenge, err := svc.Login(ctx, LoginInput{name, password}, ip)
	if err != nil {
		t.Fatal(err)
	}
	session, err := svc.MFA(ctx, MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, ip, "step-up-test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, session.Token, ip)
	if err != nil {
		t.Fatal(err)
	}
	owner := integrationOwner(t, svc)
	action := "test.stepup." + p.ID
	var role int64
	if err = owner.QueryRow(ctx, `INSERT INTO admin_roles(code,name) VALUES($1,'step up test') RETURNING id`, "stepup_"+p.ID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `INSERT INTO admin_permissions(code,name,risk_level) VALUES($1,'step up test',2)`, action); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,$2)`, role, action); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, p.ID, role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer owner.Close()
		for _, v := range []struct {
			q   string
			arg any
		}{
			{`DELETE FROM admin_user_roles WHERE role_id=$1`, role},
			{`DELETE FROM admin_role_permissions WHERE role_id=$1`, role},
			{`DELETE FROM admin_roles WHERE id=$1`, role},
			{`DELETE FROM admin_permissions WHERE code=$1`, action},
		} {
			if _, err := owner.Exec(ctx, v.q, v.arg); err != nil {
				t.Errorf("step-up fixture cleanup: %v", err)
			}
		}
	})
	clk.Add(30 * time.Second)
	return svc, clk, p, secret, ip, action
}
func issueStepUp(t *testing.T, svc *Service, clk *testClock, p Principal, secret, ip, action, hash string) StepUpResult {
	t.Helper()
	out, err := svc.IssueStepUp(context.Background(), p, StepUpInput{action, hash, currentCode(secret, clk.Now())}, ip)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestStepUpRollbackAndConcurrentUseIntegration(t *testing.T) {
	svc, clk, p, secret, ip, action := stepUpFixture(t)
	ctx := context.Background()
	hash := RequestHash("POST", "/admin/v1/users/7/status", []byte(`{"status":"SUSPENDED"}`))
	out := issueStepUp(t, svc, clk, p, secret, ip, action, hash)
	// A token bound to another target or another body cannot authorize a write.
	for _, other := range []string{RequestHash("POST", "/admin/v1/users/8/status", []byte(`{"status":"SUSPENDED"}`)), RequestHash("POST", "/admin/v1/users/7/status", []byte(`{"status":"ACTIVE"}`))} {
		tx, err := svc.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = svc.ConsumeStepUp(ctx, tx, p, out.Token, action, other, ip)
		_ = tx.Rollback(ctx)
		if err == nil {
			t.Fatal("mismatched request accepted")
		}
	}
	tx, err := svc.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.ConsumeStepUp(ctx, tx, p, out.Token, action, hash, ip); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_sessions SET user_agent='should rollback' WHERE id=$1`, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var used *time.Time
	if err = svc.Pool.QueryRow(ctx, `SELECT used_at FROM admin_step_up_tokens WHERE token_hash=$1`, tokenHash(out.Token)).Scan(&used); err != nil || used != nil {
		t.Fatal("rollback consumed token", err)
	}
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := svc.Pool.Begin(ctx)
			if e != nil {
				errs[i] = e
				return
			}
			defer tx.Rollback(ctx)
			e = svc.ConsumeStepUp(ctx, tx, p, out.Token, action, hash, ip)
			if e == nil {
				e = tx.Commit(ctx)
			}
			errs[i] = e
		}(i)
	}
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("token authorized %d concurrent writes", successes)
	}
	// Reusing the TOTP step cannot mint a second token, even for a new payload.
	if _, err = svc.IssueStepUp(ctx, p, StepUpInput{action, hash, currentCode(secret, clk.Now())}, ip); err == nil {
		t.Fatal("TOTP replay accepted")
	}
}
func TestStepUpExpiryRevocationAndSessionBindingIntegration(t *testing.T) {
	svc, clk, p, secret, ip, action := stepUpFixture(t)
	ctx := context.Background()
	hash := RequestHash("DELETE", "/admin/v1/devices/5", nil)
	out := issueStepUp(t, svc, clk, p, secret, ip, action, hash)
	consume := func(who Principal, token, requestIP string) error {
		tx, err := svc.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		return svc.ConsumeStepUp(ctx, tx, who, token, action, hash, requestIP)
	}
	wrong := p
	wrong.SessionID++
	if err := consume(wrong, out.Token, ip); err == nil {
		t.Fatal("another session accepted")
	}
	if err := consume(p, out.Token, "203.0.113.1"); err == nil {
		t.Fatal("different IP prefix accepted")
	}
	clk.Add(stepUpTTL)
	if err := consume(p, out.Token, ip); err == nil {
		t.Fatal("token accepted at expiry boundary")
	}
	out = issueStepUp(t, svc, clk, p, secret, ip, action, hash)
	if err := svc.Logout(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := consume(p, out.Token, ip); err == nil {
		t.Fatal("revoked session accepted")
	}
}
func TestStepUpPermissionRevocationAndFailedCodeLockIntegration(t *testing.T) {
	svc, clk, p, secret, ip, action := stepUpFixture(t)
	ctx := context.Background()
	hash := RequestHash("PATCH", "/admin/v1/plans/1", []byte(`{}`))
	out := issueStepUp(t, svc, clk, p, secret, ip, action, hash)
	owner := integrationOwner(t, svc)
	defer owner.Close()
	if _, err := owner.Exec(ctx, `UPDATE admin_permissions SET risk_level=0 WHERE code=$1`, action); err != nil {
		t.Fatal(err)
	}
	tx, err := svc.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = svc.ConsumeStepUp(ctx, tx, p, out.Token, action, hash, ip)
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("permission downgrade accepted")
	}
	if _, err = owner.Exec(ctx, `UPDATE admin_permissions SET risk_level=2 WHERE code=$1`, action); err != nil {
		t.Fatal(err)
	}
	clk.Add(30 * time.Second)
	// A definitely wrong, syntactically valid code consumes the persistent budget.
	code := ""
	for candidate := 0; candidate < 10; candidate++ {
		code = fmt.Sprintf("%06d", candidate)
		matches := false
		for d := -1; d <= 1; d++ {
			if currentCode(secret, clk.Now().Add(time.Duration(d)*30*time.Second)) == code {
				matches = true
			}
		}
		if !matches {
			break
		}
	}

	for i := 0; i < maxFailures; i++ {
		if _, err = svc.IssueStepUp(ctx, p, StepUpInput{action, hash, code}, ip); err == nil {
			t.Fatal("bad code accepted")
		}
	}
	var status string
	if err = svc.Pool.QueryRow(ctx, `SELECT status FROM admin_users WHERE id=$1`, p.ID).Scan(&status); err != nil || status != "LOCKED" {
		t.Fatal("failures did not lock administrator", status, err)
	}
	if _, err = svc.IssueStepUp(ctx, p, StepUpInput{action, hash, currentCode(secret, clk.Now())}, ip); err == nil {
		t.Fatal("locked administrator accepted")
	}
}
func TestStepUpRequestHash(t *testing.T) {
	a := RequestHash("DELETE", "/admin/v1/devices/1", nil)
	if len(a) != 64 {
		t.Fatal(a)
	}
	for _, b := range []string{RequestHash("DELETE", "/admin/v1/devices/2", nil), RequestHash("POST", "/admin/v1/devices/1", nil), RequestHash("DELETE", "/admin/v1/devices/1?force=1", nil), RequestHash("DELETE", "/admin/v1/devices/1", []byte(`{}`))} {
		if a == b {
			t.Fatal("request identity omitted")
		}
	}
}

func TestStepUpSessionExpiryWhilePermissionLockWaitsIntegration(t *testing.T) {
	svc, clk, p, secret, ip, action := stepUpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hash := RequestHash("POST", "/admin/v1/users/9/status", []byte(`{}`))
	out := issueStepUp(t, svc, clk, p, secret, ip, action, hash)
	owner := integrationOwner(t, svc)
	defer owner.Close()
	if _, err := owner.Exec(ctx, `UPDATE admin_sessions SET expires_at=$2 WHERE id=$1`, p.SessionID, clk.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	lock, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	var ownerPID int
	if err = lock.QueryRow(ctx, `SELECT pg_backend_pid() FROM admin_permissions WHERE code=$1 FOR UPDATE`, action).Scan(&ownerPID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		tx, e := svc.Pool.Begin(ctx)
		if e != nil {
			finished <- e
			return
		}
		defer tx.Rollback(context.Background())
		e = svc.ConsumeStepUp(ctx, tx, p, out.Token, action, hash, ip)
		if e == nil {
			e = tx.Commit(ctx)
		}
		finished <- e
	}()
	for {
		select {
		case e := <-finished:
			t.Fatalf("consume did not wait for permission lock: %v", e)
		default:
		}
		var waiting bool
		if err = owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, ownerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("consume never reached permission lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	clk.Add(2 * time.Second)
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err == nil {
		t.Fatal("session expired during lock wait was accepted")
	}
	var used *time.Time
	if err = svc.Pool.QueryRow(ctx, `SELECT used_at FROM admin_step_up_tokens WHERE token_hash=$1`, tokenHash(out.Token)).Scan(&used); err != nil || used != nil {
		t.Fatal("rejected request consumed token", err)
	}
}
