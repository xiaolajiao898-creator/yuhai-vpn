package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type approvalFixture struct {
	ctx         context.Context
	pool, owner *pgxpool.Pool
	svc         *ApprovalService
	audit       *audit.Service
	clock       *testClock
	people      [2]Principal
	sessions    [2]string
	role        int64
	schema      string
	versions    int
}

func newApprovalFixture(t *testing.T) *approvalFixture {
	t.Helper()
	return newApprovalFixtureMode(t, false)
}
func newApprovalFixtureMode(t *testing.T, actualMigration bool) *approvalFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set test application and migration URLs for isolated approval schemas")
	}
	app, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("bad application test config")
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("bad owner test config")
	}
	if app.ConnConfig.Host != ownerConfig.ConnConfig.Host || app.ConnConfig.Port != ownerConfig.ConnConfig.Port || app.ConnConfig.Database != ownerConfig.ConnConfig.Database || app.ConnConfig.Port != 15432 {
		t.Fatal("approval fixtures require the same isolated database on port 15432")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.NewWithConfig(ctx, ownerConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := "approval_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := base.Exec(c, "DROP SCHEMA "+q+" CASCADE"); err != nil {
			t.Error("fixture cleanup", err)
		}
	})
	for _, table := range []string{"admin_users", "admin_roles", "admin_permissions", "admin_role_permissions", "admin_user_roles", "admin_sessions", "admin_step_up_tokens", "admin_approvals", "admin_audit_logs", "app_versions"} {
		options := "INCLUDING ALL"
		if actualMigration && table == "admin_approvals" {
			options = "INCLUDING DEFAULTS INCLUDING IDENTITY INCLUDING CONSTRAINTS"
		}
		if _, err = base.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" "+options+")"); err != nil {
			t.Fatal(err)
		}
	}
	if actualMigration {
		migration, err := os.ReadFile("../db/migrations/00013_admin_approvals.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := base.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackApproval(tx)
		if _, err = tx.Exec(ctx, "SET LOCAL search_path TO "+q); err != nil {
			t.Fatal(err)
		}
		// Model the pre-00013 approval table even if another developer has
		// upgraded public already. No copied pending index can mask a bad migration.
		if _, err = tx.Exec(ctx, `ALTER TABLE admin_approvals DROP COLUMN IF EXISTS decision_note;
INSERT INTO admin_roles(code,name) VALUES('superadmin','existing fixture role');
INSERT INTO admin_users(username,password_hash,totp_secret_enc) VALUES('existing_admin','not-used','');
INSERT INTO admin_user_roles(admin_user_id,role_id) SELECT u.id,r.id FROM admin_users u CROSS JOIN admin_roles r;
INSERT INTO admin_permissions(code,name,risk_level) VALUES('existing.read','existing fixture permission',0);
INSERT INTO admin_role_permissions(role_id,permission_code) SELECT id,'existing.read' FROM admin_roles;`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
			t.Fatal("actual 00013 migration failed", err)
		}
		var users, roles, userRoles, grants int
		if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM admin_users),(SELECT count(*) FROM admin_roles),(SELECT count(*) FROM admin_user_roles),(SELECT count(*) FROM admin_role_permissions)`).Scan(&users, &roles, &userRoles, &grants); err != nil {
			t.Fatal(err)
		}
		if users != 1 || roles != 1 || userRoles != 1 || grants != 1 {
			t.Fatal("migration implicitly assigned capabilities", users, roles, userRoles, grants)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	} else {
		for _, sql := range []string{"ALTER TABLE " + q + ".admin_approvals ADD COLUMN IF NOT EXISTS decision_note VARCHAR(500)", "CREATE UNIQUE INDEX fixture_pending_publish ON " + q + ".admin_approvals(target_id) WHERE action_code='app_versions.publish' AND target_type='app_version' AND status='PENDING'"} {
			if _, err = base.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA " + q + " TO " + pgx.Identifier{app.ConnConfig.User}.Sanitize(),
		"GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + q + " TO " + pgx.Identifier{app.ConnConfig.User}.Sanitize(),
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + q + " TO " + pgx.Identifier{app.ConnConfig.User}.Sanitize(),
	} {
		if _, err = base.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	app.ConnConfig.RuntimeParams["search_path"] = schema
	app.ConnConfig.RuntimeParams["application_name"] = schema
	app.MaxConns = 12
	ownerConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); pool.Close(); owner.Close() })
	clk := &testClock{}
	clk.unix.Store(2100000000)
	auditSvc := &audit.Service{Pool: pool, Clock: clk}
	f := &approvalFixture{ctx: ctx, pool: pool, owner: owner, audit: auditSvc, clock: clk, schema: schema, svc: &ApprovalService{Auth: &Service{Pool: pool, Clock: clk}, Audit: auditSvc}}
	if !actualMigration {
		f.exec(t, `INSERT INTO admin_permissions(code,name,risk_level) VALUES('approvals.read','fixture',0),('approvals.decide','fixture',2),('app_versions.publish','fixture',3),('audit.read','fixture',0)`)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO admin_roles(code,name) VALUES('fixture','fixture') RETURNING id`).Scan(&f.role); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) SELECT $1,code FROM admin_permissions`, f.role)
	for i := range f.people {
		var id, sid int64
		if err = pool.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,totp_secret_enc) VALUES($1,'not-used','') RETURNING id`, fmt.Sprintf("fixture%d", i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.exec(t, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, id, f.role)
		token, err := randomToken()
		if err != nil {
			t.Fatal(err)
		}
		if err = pool.QueryRow(ctx, `INSERT INTO admin_sessions(admin_user_id,token_hash,ip,last_active_at,expires_at) VALUES($1,$2,'198.18.0.1',$3,$4) RETURNING id`, id, tokenHash(token), clk.Now(), clk.Now().Add(8*time.Hour)).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		f.people[i] = Principal{ID: strconv.FormatInt(id, 10), SessionID: sid, Permissions: []string{"approvals.read", "approvals.decide", publishAction, "audit.read"}}
		f.sessions[i] = token
	}
	return f
}
func (f *approvalFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *approvalFixture) version(t *testing.T) int64 {
	t.Helper()
	f.versions++
	var id int64
	err := f.pool.QueryRow(f.ctx, `INSERT INTO app_versions(platform,version_name,version_code,min_supported_code,download_url,file_sha256,cert_sha256) VALUES('ANDROID','1.0.0',$1,0,'https://download.example.com/app.apk',$2,$2) RETURNING id`, f.versions, strings.Repeat("a", 64)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *approvalFixture) proof(t *testing.T, person int, action, uri, body string) ApprovalProof {
	t.Helper()
	token, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	p := f.people[person]
	f.exec(t, `UPDATE admin_sessions SET last_active_at=$2,expires_at=$3 WHERE id=$1`, p.SessionID, f.clock.Now(), f.clock.Now().Add(8*time.Hour))
	hash := RequestHash("POST", uri, []byte(body))
	f.exec(t, `INSERT INTO admin_step_up_tokens(token_hash,session_id,action_code,payload_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, tokenHash(token), p.SessionID, action, hash, f.clock.Now().Add(stepUpTTL))
	return ApprovalProof{Principal: p, Token: token, RequestHash: hash, RequestID: strings.ReplaceAll(uuid.NewString(), "-", ""), IP: "198.18.0.1", UserAgent: "approval-fixture"}
}
func (f *approvalFixture) request(t *testing.T, id int64) Approval {
	t.Helper()
	p := f.proof(t, 0, publishAction, fmt.Sprintf("/admin/v1/app-versions/%d/publish", id), `{"reason":"发布更新"}`)
	out, err := f.svc.RequestPublish(f.ctx, p, id, "发布更新")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func approvalCode(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var e *httpx.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("wanted %d, got %v", code, err)
	}
}
func (f *approvalFixture) state(t *testing.T, id int64) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM app_versions WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}
func (f *approvalFixture) unused(t *testing.T, p ApprovalProof) {
	t.Helper()
	var used *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT used_at FROM admin_step_up_tokens WHERE token_hash=$1`, tokenHash(p.Token)).Scan(&used); err != nil || used != nil {
		t.Fatal("failed mutation consumed token", used, err)
	}
}

func TestApprovalPublishReuseAndAtomicExecutionIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	id, untouched := f.version(t), f.version(t)
	a := f.request(t, id)
	if a.Status != "PENDING" || a.Snapshot == nil || f.state(t, id) != "DRAFT" {
		t.Fatal("request published directly", a)
	}
	p := f.proof(t, 0, publishAction, fmt.Sprintf("/admin/v1/app-versions/%d/publish", id), `{"reason":"发布更新"}`)
	repeated, err := f.svc.RequestPublish(f.ctx, p, id, "发布更新")
	if err != nil || repeated.ID != a.ID {
		t.Fatal("request did not reuse pending", repeated, err)
	}
	aid, _ := positiveApprovalID(a.ID)
	decision := f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	out, err := f.svc.Decide(f.ctx, decision, aid, true, "")
	if err != nil || out.Status != "EXECUTED" || out.DecidedBy == nil || *out.DecidedBy != f.people[1].ID || out.ExecutedAt == nil {
		t.Fatal(out, err)
	}
	if f.state(t, id) != "PUBLISHED" || f.state(t, untouched) != "DRAFT" {
		t.Fatal("wrong version changed")
	}
	var linked int64
	var at time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT approval_id,published_at FROM app_versions WHERE id=$1`, id).Scan(&linked, &at); err != nil || linked != aid || !at.Equal(f.clock.Now()) {
		t.Fatal("publication linkage lost", linked, at, err)
	}
	_, err = f.svc.Decide(f.ctx, decision, aid, true, "")
	approvalCode(t, err, httpx.ErrStepUpInvalid)
	logs, err := f.audit.List(f.ctx, 1, 50, "", "")
	if err != nil || logs.Total != "4" {
		t.Fatal(logs, err)
	}
	first, _ := strconv.ParseInt(logs.Items[len(logs.Items)-1].ID, 10, 64)
	last, _ := strconv.ParseInt(logs.Items[0].ID, 10, 64)
	verified, err := f.audit.Verify(f.ctx, first, last)
	if err != nil || !verified.Valid {
		t.Fatal("audit chain invalid", verified, err)
	}
}
func TestApprovalSelfPermissionSnapshotAndUnknownExecutorIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	id := f.version(t)
	a := f.request(t, id)
	aid, _ := positiveApprovalID(a.ID)
	own := f.proof(t, 0, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	_, err := f.svc.Decide(f.ctx, own, aid, true, "")
	approvalCode(t, err, httpx.ErrApproverIsAuthor)
	f.unused(t, own)
	next := func() ApprovalProof {
		return f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	}
	p := next()
	p.RequestHash = RequestHash("POST", "/admin/v1/approvals/999/approve", []byte(`{}`))
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrStepUpInvalid)
	f.exec(t, `UPDATE admin_users SET status='DISABLED' WHERE id=$1`, f.people[0].ID)
	p = next()
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrForbidden)
	f.unused(t, p)
	f.exec(t, `UPDATE admin_users SET status='ACTIVE' WHERE id=$1`, f.people[0].ID)
	if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_user_roles WHERE admin_user_id=$1`, f.people[0].ID); err != nil {
		t.Fatal(err)
	}
	p = next()
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrForbidden)
	f.exec(t, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, f.people[0].ID, f.role)
	f.exec(t, `UPDATE app_versions SET release_notes='edited after request' WHERE id=$1`, id)
	p = next()
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrApprovalStale)
	f.unused(t, p)
	if f.state(t, id) != "DRAFT" {
		t.Fatal("denied approval changed version")
	}
	f.exec(t, `UPDATE admin_approvals SET action_code='balance.adjust',target_type='balance',payload='{"secret":"never-return"}' WHERE id=$1`, aid)
	detail, err := f.svc.Get(f.ctx, f.people[1], aid)
	raw, _ := json.Marshal(detail)
	if err != nil || detail.ExecutorSupported || detail.Snapshot != nil || strings.Contains(string(raw), "never-return") {
		t.Fatal("unknown payload exposed", detail, err)
	}
	p = next()
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrMaintenance)
	p = f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/reject", `{"note":"尚未支持，退回"}`)
	rejected, err := f.svc.Decide(f.ctx, p, aid, false, "尚未支持，退回")
	if err != nil || rejected.Status != "REJECTED" || rejected.DecisionNote == nil || *rejected.DecisionNote != "尚未支持，退回" {
		t.Fatal(rejected, err)
	}
	var note string
	if err = f.pool.QueryRow(f.ctx, `SELECT after_data->>'decision_note' FROM admin_audit_logs WHERE result='SUCCESS' ORDER BY id DESC LIMIT 1`).Scan(&note); err != nil || note != *rejected.DecisionNote {
		t.Fatal("rejection note not audited", note, err)
	}
}
func TestApprovalExpiryFiltersAndConflictingRequestsIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	id := f.version(t)
	a := f.request(t, id)
	aid, _ := positiveApprovalID(a.ID)
	p := f.proof(t, 0, publishAction, fmt.Sprintf("/admin/v1/app-versions/%d/publish", id), `{"reason":"不同理由"}`)
	_, err := f.svc.RequestPublish(f.ctx, p, id, "不同理由")
	approvalCode(t, err, httpx.ErrConflict)
	f.exec(t, `UPDATE app_versions SET release_notes='changed' WHERE id=$1`, id)
	p = f.proof(t, 0, publishAction, fmt.Sprintf("/admin/v1/app-versions/%d/publish", id), `{"reason":"发布更新"}`)
	_, err = f.svc.RequestPublish(f.ctx, p, id, "发布更新")
	approvalCode(t, err, httpx.ErrApprovalStale)
	list, err := f.svc.List(f.ctx, f.people[1], ApprovalFilter{})
	if err != nil || list.Total != "1" || len(list.Items) != 1 || list.Items[0].Snapshot != nil {
		t.Fatal(list, err)
	}
	list, err = f.svc.List(f.ctx, f.people[0], ApprovalFilter{})
	if err != nil || list.Total != "0" {
		t.Fatal("pending scope included own", list, err)
	}
	f.clock.Add(approvalTTL)
	p = f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	_, err = f.svc.Decide(f.ctx, p, aid, true, "")
	approvalCode(t, err, httpx.ErrApprovalStale)
	list, err = f.svc.List(f.ctx, f.people[1], ApprovalFilter{Scope: "all", Status: "EXPIRED"})
	if err != nil || list.Total != "1" || list.Items[0].Status != "EXPIRED" {
		t.Fatal(list, err)
	}
	replacement := f.request(t, id)
	if replacement.ID == a.ID || replacement.Status != "PENDING" {
		t.Fatal("expired pending reused", replacement)
	}
}

type approvalAuditFunc func(context.Context, pgx.Tx, audit.Event) error

func (fn approvalAuditFunc) Append(ctx context.Context, tx pgx.Tx, e audit.Event) error {
	return fn(ctx, tx, e)
}
func TestApprovalAuditFailureAndFinalLockExpiryRollbackIntegration(t *testing.T) {
	for _, mode := range []string{"failure", "late_token"} {
		t.Run(mode, func(t *testing.T) {
			f := newApprovalFixture(t)
			id := f.version(t)
			a := f.request(t, id)
			aid, _ := positiveApprovalID(a.ID)
			p := f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/reject", `{"note":"验证退回"}`)
			f.svc.Audit = approvalAuditFunc(func(ctx context.Context, tx pgx.Tx, e audit.Event) error {
				if e.Result == "SUCCESS" {
					if mode == "failure" {
						return errors.New("unsafe private database message")
					}
					f.clock.Add(stepUpTTL)
				}
				return f.audit.Append(ctx, tx, e)
			})
			_, err := f.svc.Decide(f.ctx, p, aid, false, "验证退回")
			if mode == "failure" {
				approvalCode(t, err, httpx.ErrInternal)
			} else {
				approvalCode(t, err, httpx.ErrStepUpInvalid)
			}
			if strings.Contains(err.Error(), "unsafe private") {
				t.Fatal("raw database error returned")
			}
			f.unused(t, p)
			var status string
			var note *string
			if err = f.pool.QueryRow(f.ctx, `SELECT status,decision_note FROM admin_approvals WHERE id=$1`, aid).Scan(&status, &note); err != nil || status != "PENDING" || note != nil {
				t.Fatal("audit failure left rejection committed", status, note, err)
			}
			var failed int
			if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM admin_audit_logs WHERE result IN ('FAILED','DENIED')`).Scan(&failed); err != nil || failed != 1 {
				t.Fatal("failure audit missing", failed, err)
			}
		})
	}
}
func TestApprovalConcurrentDecisionsExecuteOnlyOnceIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	id := f.version(t)
	a := f.request(t, id)
	aid, _ := positiveApprovalID(a.ID)
	const n = 6
	proofs := make([]ApprovalProof, n)
	for i := range proofs {
		proofs[i] = f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range proofs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = f.svc.Decide(f.ctx, proofs[i], aid, true, "") }(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			approvalCode(t, err, httpx.ErrConflict)
		}
	}
	if succeeded != 1 || f.state(t, id) != "PUBLISHED" {
		t.Fatal("duplicate execution", succeeded, errs)
	}
	var executions int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM admin_audit_logs WHERE action_code='approvals.decide' AND result='SUCCESS'`).Scan(&executions); err != nil || executions != 1 {
		t.Fatal("duplicate audit execution", executions, err)
	}
}
func TestApprovalExpiryWhileWaitingForVersionLockIntegration(t *testing.T) {
	for _, mode := range []string{"token", "session", "approval"} {
		t.Run(mode, func(t *testing.T) {
			f := newApprovalFixture(t)
			id := f.version(t)
			a := f.request(t, id)
			aid, _ := positiveApprovalID(a.ID)
			p := f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
			if mode == "session" {
				f.exec(t, `UPDATE admin_sessions SET expires_at=$2 WHERE id=$1`, p.Principal.SessionID, f.clock.Now().Add(time.Second))
			}
			if mode == "approval" {
				f.exec(t, `UPDATE admin_approvals SET expires_at=$2 WHERE id=$1`, aid, f.clock.Now().Add(time.Second))
			}
			lock, err := f.owner.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackApproval(lock)
			var pid int
			if err = lock.QueryRow(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = lock.Exec(f.ctx, `SELECT id FROM app_versions WHERE id=$1 FOR UPDATE`, id); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { _, err := f.svc.Decide(f.ctx, p, aid, true, ""); finished <- err }()
			for {
				var waiting bool
				if err = f.owner.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid)))`, f.schema, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-finished:
					t.Fatal("did not wait", err)
				case <-f.ctx.Done():
					t.Fatal("wait timeout")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if mode == "token" {
				f.clock.Add(stepUpTTL)
			} else {
				f.clock.Add(2 * time.Second)
			}
			if err = lock.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = <-finished
			if mode == "approval" {
				approvalCode(t, err, httpx.ErrApprovalStale)
			} else {
				approvalCode(t, err, httpx.ErrStepUpInvalid)
			}
			f.unused(t, p)
			if f.state(t, id) != "DRAFT" {
				t.Fatal("expired wait published version")
			}
		})
	}
}
func TestApprovalHTTPBindingPermissionsAndRawShapeIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	authHandler := &Handler{Service: f.svc.Auth}
	h := &ApprovalHandler{Auth: authHandler, Service: f.svc, Audit: f.audit}
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	g := r.Group("/admin/v1")
	authHandler.Register(g)
	h.Register(g)
	id := f.version(t)
	path := fmt.Sprintf("/admin/v1/app-versions/%d/publish", id)
	body := `{"reason":"发布更新"}`
	p := f.proof(t, 0, publishAction, path, body)
	request := func(method, path, body, token string, person int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		if person >= 0 {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[person]})
			req.Header.Set("X-CSRF-Token", csrfToken(f.sessions[person]))
		}
		req.Header.Set("X-Step-Up-Token", token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := request("POST", path, body, p.Token, -1); w.Code != 401 {
		t.Fatal("anonymous publication accepted", w.Code)
	}
	w := request("POST", path, body, p.Token, 0)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var a map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || a["id"] == nil || a["data"] != nil {
		t.Fatal("wrong raw response", a, err)
	}
	w = request("GET", "/admin/v1/approvals?scope=all", "", "", 0)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/admin/v1/approvals?scope=all&scope=mine", "", "", 0); w.Code != 400 {
		t.Fatal("duplicate query accepted", w.Code)
	}
	w = request("POST", path, `{"reason":"x","extra":"never-log-this"}`, "", 0)
	if w.Code != 400 || strings.Contains(w.Body.String(), "never-log-this") {
		t.Fatal("unknown fields accepted/exposed", w.Code, w.Body.String())
	}
	approvalID, ok := a["id"].(string)
	if !ok {
		t.Fatal("approval ID must be a string", a)
	}
	approvePath := "/admin/v1/approvals/" + approvalID + "/approve"
	wrongAction := f.proof(t, 1, publishAction, approvePath, `{}`)
	w = request("POST", approvePath, `{}`, wrongAction.Token, 1)
	if w.Code != 401 || !strings.Contains(w.Body.String(), `40111`) {
		t.Fatal("another action's token approved", w.Code, w.Body.String())
	}
	tampered := f.proof(t, 1, approvalDecision, approvePath, `{}`)
	w = request("POST", approvePath, ` { } `, tampered.Token, 1)
	if w.Code != 401 || !strings.Contains(w.Body.String(), `40111`) {
		t.Fatal("changed original body accepted", w.Code, w.Body.String())
	}
	f.unused(t, tampered)
	proper := f.proof(t, 1, approvalDecision, approvePath, `{}`)
	noCSRF := httptest.NewRequest("POST", approvePath, strings.NewReader(`{}`))
	noCSRF.RemoteAddr = "198.18.0.1:1234"
	noCSRF.Header.Set("Content-Type", "application/json")
	noCSRF.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[1]})
	noCSRF.Header.Set("X-Step-Up-Token", proper.Token)
	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, noCSRF)
	if denied.Code != 403 {
		t.Fatal("missing CSRF accepted", denied.Code)
	}
	f.unused(t, proper)
	w = request("POST", approvePath, `{}`, proper.Token, 1)
	var approved Approval
	if err := json.Unmarshal(w.Body.Bytes(), &approved); err != nil || w.Code != 200 || approved.Status != "EXECUTED" || f.state(t, id) != "PUBLISHED" {
		t.Fatal("HTTP approval did not execute", w.Code, w.Body.String(), err)
	}

	secondID := f.version(t)
	secondPath := fmt.Sprintf("/admin/v1/app-versions/%d/publish", secondID)
	secondProof := f.proof(t, 0, publishAction, secondPath, body)
	w = request("POST", secondPath, body, secondProof.Token, 0)
	var second Approval
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil || w.Code != 202 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	rejectPath := "/admin/v1/approvals/" + second.ID + "/reject"
	rejectBody := `{"note":"请先补充发布说明"}`
	rejectProof := f.proof(t, 1, approvalDecision, rejectPath, rejectBody)
	w = request("POST", rejectPath, rejectBody, rejectProof.Token, 1)
	var rejected Approval
	if err := json.Unmarshal(w.Body.Bytes(), &rejected); err != nil || w.Code != 200 || rejected.Status != "REJECTED" || rejected.DecisionNote == nil || *rejected.DecisionNote != "请先补充发布说明" || f.state(t, secondID) != "DRAFT" {
		t.Fatal("HTTP rejection failed", w.Code, w.Body.String(), err)
	}
	w = request("GET", "/admin/v1/approvals/"+second.ID, "", "", 1)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "请先补充发布说明") {
		t.Fatal("rejection detail was not persisted", w.Code, w.Body.String())
	}

	if _, err := f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='approvals.read'`, f.role); err != nil {
		t.Fatal(err)
	}
	if w = request("GET", "/admin/v1/approvals?scope=all", "", "", 1); w.Code != 403 {
		t.Fatal("missing read capability accepted", w.Code, w.Body.String())
	}
	if _, err := f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='approvals.decide'`, f.role); err != nil {
		t.Fatal(err)
	}
	if w = request("POST", "/admin/v1/approvals/9223372036854775807/approve", `{}`, "", 1); w.Code != 403 {
		t.Fatal("unprivileged approval existence probe", w.Code, w.Body.String())
	}
	w = request("GET", "/admin/v1/audit-logs?page_size=50", "", "", 1)
	var entries audit.ListResult
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil || w.Code != 200 || len(entries.Items) < 5 || entries.Total == "" {
		t.Fatal("audit list route failed", w.Code, w.Body.String(), err)
	}
	from, to := entries.Items[len(entries.Items)-1].ID, entries.Items[0].ID
	verifyBody, _ := json.Marshal(map[string]string{"from_id": from, "to_id": to})
	w = request("POST", "/admin/v1/audit-logs/verify", string(verifyBody), "", 1)
	var verification audit.VerifyResult
	if err := json.Unmarshal(w.Body.Bytes(), &verification); err != nil || w.Code != 200 || !verification.Valid || verification.FromID != from || verification.ToID != to {
		t.Fatal("audit verification route failed", w.Code, w.Body.String(), err)
	}
	w = request("POST", "/admin/v1/audit-logs/verify", `{"from_id":1,"to_id":2}`, "", 1)
	if w.Code != 400 {
		t.Fatal("numeric audit IDs accepted", w.Code)
	}
	if _, err := f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='audit.read'`, f.role); err != nil {
		t.Fatal(err)
	}
	if w = request("GET", "/admin/v1/audit-logs", "", "", 1); w.Code != 403 {
		t.Fatal("audit list permission bypass", w.Code)
	}
	if w = request("POST", "/admin/v1/audit-logs/verify", string(verifyBody), "", 1); w.Code != 403 {
		t.Fatal("audit verify permission bypass", w.Code)
	}
}

func TestApprovalListUsesNumericIDsForEqualTimestampsIntegration(t *testing.T) {
	f := newApprovalFixture(t)
	var ids []int64
	for i := 0; i < 12; i++ {
		var id int64
		err := f.pool.QueryRow(f.ctx, `INSERT INTO admin_approvals(action_code,target_type,payload,payload_hash,reason,requested_by,requested_at,expires_at) VALUES('unsupported.fixture','fixture','{}',$1,'fixture',$2,$3,$4) RETURNING id`, strings.Repeat("a", 64), f.people[0].ID, f.clock.Now(), f.clock.Now().Add(approvalTTL)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for page := 1; page <= 3; page++ {
		result, err := f.svc.List(f.ctx, f.people[1], ApprovalFilter{Scope: "all", Page: page, PageSize: 5})
		if err != nil || result.Total != "12" {
			t.Fatal(result, err)
		}
		for i, item := range result.Items {
			expected := ids[len(ids)-1-(page-1)*5-i]
			if item.ID != strconv.FormatInt(expected, 10) {
				t.Fatal("IDs sorted lexically", item.ID, expected)
			}
		}
	}
}

func TestApprovalActualMigrationIsScopedAndGrantsNoRolesIntegration(t *testing.T) {
	f := newApprovalFixtureMode(t, true)
	var assigned int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM admin_role_permissions rp JOIN admin_roles r ON r.id=rp.role_id WHERE r.code='superadmin' AND rp.permission_code IN ('approvals.read','approvals.decide','app_versions.publish','audit.read')`).Scan(&assigned); err != nil || assigned != 0 {
		t.Fatal("migration implicitly granted existing administrator permissions", assigned, err)
	}
	for permission, risk := range map[string]int{"approvals.read": 0, "approvals.decide": 2, publishAction: 3, "audit.read": 0} {
		var got int
		if err := f.pool.QueryRow(f.ctx, `SELECT risk_level FROM admin_permissions WHERE code=$1`, permission).Scan(&got); err != nil || got != risk {
			t.Fatal("migration capability definition incorrect", permission, got, err)
		}
	}
	id := f.version(t)
	a := f.request(t, id)
	insert := func() error {
		_, err := f.pool.Exec(f.ctx, `INSERT INTO admin_approvals(action_code,target_type,target_id,payload,payload_hash,reason,requested_by,requested_at,expires_at) VALUES('app_versions.publish','app_version',$1,'{}',$2,'fixture',$3,$4,$5)`, id, strings.Repeat("a", 64), f.people[0].ID, f.clock.Now(), f.clock.Now().Add(approvalTTL))
		return err
	}
	err := insert()
	var dbErr *pgconn.PgError
	if !errors.As(err, &dbErr) || dbErr.Code != "23505" || dbErr.ConstraintName != "idx_admin_pending_version_publish" {
		t.Fatal("actual migration did not enforce pending uniqueness", err)
	}
	f.exec(t, `UPDATE admin_approvals SET status='EXPIRED' WHERE id=$1`, a.ID)
	if err := insert(); err != nil {
		t.Fatal("terminal approval still blocked new pending", err)
	}
}
