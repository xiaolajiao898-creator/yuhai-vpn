package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type managementFixture struct {
	*approvalFixture
	managed *AdminManagementService
	reader  int64
}

func newManagementFixture(t *testing.T) *managementFixture {
	t.Helper()
	base := newApprovalFixture(t)
	raw, err := os.ReadFile("../db/migrations/00038_admin_management.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err = base.owner.Exec(base.ctx, up); err != nil {
		t.Fatal("management migration", err)
	}
	var grants int
	if err = base.owner.QueryRow(base.ctx, `SELECT count(*) FROM admin_role_permissions WHERE permission_code LIKE 'admins.%'`).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("implicit permission grant", grants, err)
	}
	// Production access is admin-only; fixture uses the application role explicitly.
	if _, err = base.owner.Exec(base.ctx, `GRANT SELECT,INSERT,UPDATE ON admin_management_requests TO `+pgx.Identifier{base.pool.Config().ConnConfig.User}.Sanitize()+`; GRANT DELETE ON admin_user_roles TO `+pgx.Identifier{base.pool.Config().ConnConfig.User}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	base.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) SELECT $1,code FROM admin_permissions WHERE code LIKE 'admins.%'`, base.role)
	for i := range base.people {
		base.people[i].Permissions = append(base.people[i].Permissions, adminRead, actionAdminCreate, actionAdminRoles)
	}
	enc, err := fieldenc.New("management-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	base.svc.Auth.Enc = enc
	base.clock.unix.Store(2000000000)
	f := &managementFixture{approvalFixture: base, managed: &AdminManagementService{Security: base.svc}}
	if err = base.owner.QueryRow(base.ctx, `INSERT INTO admin_roles(code,name) VALUES('reader','Read only') RETURNING id`).Scan(&f.reader); err != nil {
		t.Fatal(err)
	}
	base.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'audit.read')`, f.reader)
	return f
}
func (f *managementFixture) input() AdminCreateInput {
	return AdminCreateInput{OperationID: uuid.NewString(), Reason: "创建审计人员", Username: "audit.member", Password: "a fresh long test password", TOTPSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", TOTPCode: "279037", RoleIDs: []string{adminID(f.reader)}}
}
func (f *managementFixture) createProof(t *testing.T, person int, in AdminCreateInput) ApprovalProof {
	t.Helper()
	raw, _ := json.Marshal(in)
	return f.proof(t, person, actionAdminCreate, "/admin/v1/admins", string(raw))
}
func (f *managementFixture) create(t *testing.T, in AdminCreateInput) (Approval, error) {
	t.Helper()
	return f.managed.RequestCreate(f.ctx, f.createProof(t, 0, in), in)
}
func (f *managementFixture) decide(t *testing.T, person int, id string, approve bool) (Approval, error) {
	t.Helper()
	aid, _ := positiveApprovalID(id)
	body := `{"approve":true}`
	note := ""
	if !approve {
		body = `{"approve":false,"note":"取消"}`
		note = "取消"
	}
	p := f.proof(t, person, approvalDecision, "/admin/v1/approvals/"+id+"/decision", body)
	return f.svc.Decide(f.ctx, p, aid, approve, note)
}
func (f *managementFixture) target(t *testing.T, role int64) (int64, int64) {
	t.Helper()
	var id, sid int64
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO admin_users(username,password_hash,totp_secret_enc) VALUES('target.user','unused','') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, id, role)
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO admin_sessions(admin_user_id,token_hash,ip,last_active_at,expires_at) VALUES($1,$2,'198.18.0.1',$3,$4) RETURNING id`, id, tokenHash("target-session"), f.clock.Now(), f.clock.Now().Add(time.Hour)).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	return id, sid
}
func (f *managementFixture) roles(t *testing.T, person int, target int64, ids []string) (Approval, error) {
	t.Helper()
	in := AdminRolesInput{OperationID: uuid.NewString(), Reason: "更新职责", RoleIDs: ids}
	raw, _ := json.Marshal(in)
	p := f.proof(t, person, actionAdminRoles, fmt.Sprintf("/admin/v1/admins/%d/roles", target), string(raw))
	return f.managed.RequestRoles(f.ctx, p, target, in)
}

func TestAdminManagementCreationApprovalsSecretsAndReplayIntegration(t *testing.T) {
	f := newManagementFixture(t)
	in := f.input()
	p := f.createProof(t, 0, in)
	a, err := f.managed.RequestCreate(f.ctx, p, in)
	if err != nil || a.Status != "PENDING" || !a.ExecutorSupported {
		t.Fatal(a, err)
	}
	again, err := f.managed.RequestCreate(f.ctx, p, in)
	if err != nil || again.ID != a.ID {
		t.Fatal("idempotent replay", again, err)
	}
	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_users WHERE username=$1`, in.Username).Scan(&count); err != nil || count != 0 {
		t.Fatal("created before approval", count, err)
	}
	var records, success int
	if err = f.owner.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM admin_management_requests),(SELECT count(*) FROM admin_audit_logs WHERE action_code='admins.create' AND result='SUCCESS')`).Scan(&records, &success); err != nil || records != 1 || success != 1 {
		t.Fatal("duplicate records or audit", records, success, err)
	}
	var public string
	if err = f.owner.QueryRow(f.ctx, `SELECT payload::text FROM admin_approvals WHERE id=$1`, a.ID).Scan(&public); err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(a)
	for _, secret := range []string{in.Password, in.TOTPSecret, "password_hash", "totp_secret", "credentials_enc"} {
		if strings.Contains(public, secret) || strings.Contains(string(response), secret) {
			t.Fatal("secret disclosed", secret)
		}
	}
	var secretEnc []byte
	if err = f.owner.QueryRow(f.ctx, `SELECT credentials_enc FROM admin_management_requests`).Scan(&secretEnc); err != nil || strings.Contains(string(secretEnc), in.TOTPSecret) || strings.Contains(string(secretEnc), in.Password) {
		t.Fatal("unencrypted staging", err)
	}
	if a.Snapshot == nil || a.Snapshot["username"] == nil || *a.Snapshot["username"] != in.Username {
		t.Fatal("missing reviewer snapshot", a)
	}
	_, err = f.managed.RequestCreate(f.ctx, f.createProof(t, 1, in), in)
	nodeCode(t, err, httpx.ErrConflict)
	altered := in
	altered.Username = "another.member"
	_, err = f.managed.RequestCreate(f.ctx, f.createProof(t, 0, altered), altered)
	nodeCode(t, err, httpx.ErrConflict)
	done, err := f.decide(t, 1, a.ID, true)
	if err != nil || done.Status != "EXECUTED" || done.TargetID == nil {
		t.Fatal("create execution", done, err)
	}
	var storedHash string
	var encrypted []byte
	var last int64
	if err = f.owner.QueryRow(f.ctx, `SELECT password_hash,totp_secret_enc,totp_last_step FROM admin_users WHERE id=$1`, *done.TargetID).Scan(&storedHash, &encrypted, &last); err != nil || !VerifyPassword(storedHash, in.Password) || last != 2000000000/30 {
		t.Fatal("stored credentials", err, last)
	}
	id, _ := positiveApprovalID(*done.TargetID)
	plain, err := f.svc.Auth.Enc.Decrypt(encrypted, fieldenc.AAD("admin_users", "totp_secret_enc", id))
	if err != nil || string(plain) != in.TOTPSecret {
		t.Fatal("TOTP encryption AAD", err)
	}
	if err = f.owner.QueryRow(f.ctx, `SELECT credentials_enc FROM admin_management_requests`).Scan(&secretEnc); err != nil || secretEnc != nil {
		t.Fatal("staging not wiped", err)
	}
	replay, err := f.managed.RequestCreate(f.ctx, p, in)
	if err != nil || replay.Status != "EXECUTED" || replay.ID != a.ID {
		t.Fatal("post-execution replay", replay, err)
	}
	_, err = f.decide(t, 1, a.ID, true)
	nodeCode(t, err, httpx.ErrConflict)
}
func TestAdminManagementRolesReplaceRevokeAndStaleIntegration(t *testing.T) {
	f := newManagementFixture(t)
	target, sid := f.target(t, f.reader)
	f.exec(t, `UPDATE admin_users SET username='Legacy.Admin' WHERE id=$1`, target)
	first, err := f.roles(t, 0, target, []string{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.roles(t, 0, target, []string{adminID(f.role)})
	if err != nil {
		t.Fatal(err)
	}
	done, err := f.decide(t, 1, first.ID, true)
	if err != nil || done.Status != "EXECUTED" {
		t.Fatal(done, err)
	}
	var roles int
	var revoked *time.Time
	if err = f.owner.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM admin_user_roles WHERE admin_user_id=$1),revoked_at FROM admin_sessions WHERE id=$2`, target, sid).Scan(&roles, &revoked); err != nil || roles != 0 || revoked == nil {
		t.Fatal("role replacement not atomic with revocation", roles, revoked, err)
	}
	_, err = f.decide(t, 1, second.ID, true)
	nodeCode(t, err, httpx.ErrApprovalStale)
	var status string
	if err = f.owner.QueryRow(f.ctx, `SELECT status FROM admin_approvals WHERE id=$1`, second.ID).Scan(&status); err != nil || status != "PENDING" {
		t.Fatal("stale approval partially executed", status, err)
	}
}
func TestAdminManagementNoSelfRoleChangeIntegration(t *testing.T) {
	f := newManagementFixture(t)
	self, _ := positiveApprovalID(f.people[0].ID)
	_, err := f.roles(t, 0, self, []string{})
	nodeCode(t, err, httpx.ErrForbidden)
	target, _ := positiveApprovalID(f.people[1].ID)
	a, err := f.roles(t, 0, target, []string{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.decide(t, 1, a.ID, true)
	nodeCode(t, err, httpx.ErrForbidden)
	a, err = f.create(t, f.input())
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.decide(t, 0, a.ID, true)
	nodeCode(t, err, httpx.ErrApproverIsAuthor)
}
func TestAdminManagementCannotGrantUnheldRolesIntegration(t *testing.T) {
	f := newManagementFixture(t)
	f.exec(t, `INSERT INTO admin_permissions(code,name,risk_level) VALUES('private.power','private',3)`)
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'private.power')`, f.reader)
	_, err := f.create(t, f.input())
	nodeCode(t, err, httpx.ErrForbidden)
	target, _ := f.target(t, f.reader)
	_, err = f.roles(t, 0, target, []string{})
	nodeCode(t, err, httpx.ErrForbidden)
}
func TestAdminManagementRechecksRolesAndAuthorityIntegration(t *testing.T) {
	for _, change := range []string{"role_definition", "requester_authority", "approver_authority", "payload", "ciphertext"} {
		t.Run(change, func(t *testing.T) {
			f := newManagementFixture(t)
			in := f.input()
			a, err := f.create(t, in)
			if err != nil {
				t.Fatal(err)
			}
			code := httpx.ErrApprovalStale
			switch change {
			case "role_definition":
				f.exec(t, `UPDATE admin_roles SET name='changed' WHERE id=$1`, f.reader)
			case "requester_authority":
				if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_user_roles WHERE admin_user_id=$1`, f.people[0].ID); err != nil {
					t.Fatal(err)
				}
				code = httpx.ErrForbidden
			case "approver_authority":
				if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='admins.create'`, f.role); err != nil {
					t.Fatal(err)
				}
				code = httpx.ErrForbidden
			case "payload":
				f.exec(t, `UPDATE admin_approvals SET payload=jsonb_set(payload,'{username}','"hijacked.member"') WHERE id=$1`, a.ID)
			case "ciphertext":
				f.exec(t, `UPDATE admin_management_requests SET credentials_enc='broken'::bytea WHERE approval_id=$1`, a.ID)
			}
			_, err = f.decide(t, 1, a.ID, true)
			nodeCode(t, err, code)
			var count int
			if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_users WHERE username IN ($1,'hijacked.member')`, in.Username).Scan(&count); err != nil || count != 0 {
				t.Fatal("unapproved administrator inserted", count, err)
			}
		})
	}
}
func TestAdminManagementApprovalRejectWipesCredentialsIntegration(t *testing.T) {
	f := newManagementFixture(t)
	a, err := f.create(t, f.input())
	if err != nil {
		t.Fatal(err)
	}
	done, err := f.decide(t, 1, a.ID, false)
	if err != nil || done.Status != "REJECTED" {
		t.Fatal(done, err)
	}
	var enc []byte
	if err = f.owner.QueryRow(f.ctx, `SELECT credentials_enc FROM admin_management_requests WHERE approval_id=$1`, a.ID).Scan(&enc); err != nil || enc != nil {
		t.Fatal("rejected secret retained", err)
	}
}
func TestAdminManagementReadProjectionAndPermissionIntegration(t *testing.T) {
	f := newManagementFixture(t)
	f.proof(t, 0, actionAdminCreate, "/admin/v1/admins", `{}`)
	page, err := f.managed.List(f.ctx, f.people[0], ManagedAdminFilter{PageSize: 1})
	if err != nil || len(page.Items) != 1 || page.Total != "2" || len(page.Roles) != 2 {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"password", "totp", "token", "session", "last_login_ip", "failed_attempts"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private field in projection", secret)
		}
	}
	if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='admins.read'`, f.role); err != nil {
		t.Fatal(err)
	}
	_, err = f.managed.List(f.ctx, f.people[0], ManagedAdminFilter{})
	nodeCode(t, err, httpx.ErrForbidden)
}
func TestAdminManagementInputValidationAndStepUpIntegration(t *testing.T) {
	f := newManagementFixture(t)
	for _, kind := range []string{"bad_username", "short_password", "bad_totp", "lowercase_seed", "null_roles", "empty_roles", "duplicate_roles", "missing_role"} {
		t.Run(kind, func(t *testing.T) {
			in := f.input()
			code := httpx.ErrBadRequest
			switch kind {
			case "bad_username":
				in.Username = "admin injection\n"
			case "short_password":
				in.Password = "short"
			case "bad_totp":
				in.TOTPCode = "000000"
				code = httpx.ErrTOTPInvalid
			case "lowercase_seed":
				in.TOTPSecret = strings.ToLower(in.TOTPSecret)
			case "null_roles":
				in.RoleIDs = nil
			case "empty_roles":
				in.RoleIDs = []string{}
			case "duplicate_roles":
				in.RoleIDs = append(in.RoleIDs, in.RoleIDs[0])
			case "missing_role":
				in.RoleIDs = []string{"9999"}
			}
			_, err := f.create(t, in)
			nodeCode(t, err, code)
		})
	}
	in := f.input()
	p := f.createProof(t, 0, in)
	p.Token = ""
	_, err := f.managed.RequestCreate(f.ctx, p, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)
	var n int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_approvals`).Scan(&n); err != nil || n != 0 {
		t.Fatal("invalid input wrote approval", n, err)
	}
}
func TestAdminManagementConcurrentIdempotencyIntegration(t *testing.T) {
	f := newManagementFixture(t)
	in := f.input()
	p := f.createProof(t, 0, in)
	results := make(chan Approval, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a, err := f.managed.RequestCreate(f.ctx, p, in); results <- a; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	first := ""
	for a := range results {
		if first != "" && first != a.ID {
			t.Fatal("duplicate approval", a.ID, first)
		}
		first = a.ID
	}
	var count int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_management_requests`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestAdminManagementHTTPUsesPUTBoundProofAndCSRFIntegration(t *testing.T) {
	f := newManagementFixture(t)
	target, _ := f.target(t, f.reader)
	gin.SetMode(gin.TestMode)
	handler := &Handler{Service: f.svc.Auth}
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	(&AdminManagementHandler{Auth: handler, Service: f.managed}).Register(r.Group("/admin/v1"))
	in := AdminRolesInput{OperationID: uuid.NewString(), Reason: "轮岗", RoleIDs: []string{}}
	body, _ := json.Marshal(in)
	uri := fmt.Sprintf("/admin/v1/admins/%d/roles", target)
	p := f.proof(t, 0, actionAdminRoles, uri, string(body))
	f.exec(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), RequestHash("PUT", uri, body))
	request := func(csrf bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, uri, strings.NewReader(string(body)))
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Step-Up-Token", p.Token)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
		if csrf {
			req.Header.Set("X-CSRF-Token", csrfToken(f.sessions[0]))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	denied := request(false)
	if denied.Code < 400 {
		t.Fatal("missing CSRF accepted", denied.Code)
	}
	response := request(true)
	if response.Code != http.StatusAccepted || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("PUT route", response.Code, response.Body.String())
	}
}

func TestAdminManagementAuditFailureRollsBackCredentialsAndAccountIntegration(t *testing.T) {
	for _, phase := range []string{"request", "execution"} {
		t.Run(phase, func(t *testing.T) {
			f := newManagementFixture(t)
			in := f.input()
			var a Approval
			var err error
			if phase == "execution" {
				a, err = f.create(t, in)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.svc.Audit = approvalAuditFunc(func(ctx context.Context, tx pgx.Tx, e audit.Event) error {
				if e.Result == "SUCCESS" {
					return errors.New("sensitive database error must not escape")
				}
				return f.audit.Append(ctx, tx, e)
			})
			if phase == "request" {
				_, err = f.create(t, in)
			} else {
				_, err = f.decide(t, 1, a.ID, true)
			}
			nodeCode(t, err, httpx.ErrInternal)
			if strings.Contains(err.Error(), "sensitive database") {
				t.Fatal("raw error disclosed")
			}
			var users, requests int
			if err = f.owner.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM admin_users WHERE username=$1),(SELECT count(*) FROM admin_management_requests)`, in.Username).Scan(&users, &requests); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if phase == "execution" {
				expected = 1
				var status string
				var encrypted []byte
				if err = f.owner.QueryRow(f.ctx, `SELECT a.status,r.credentials_enc FROM admin_approvals a JOIN admin_management_requests r ON r.approval_id=a.id WHERE a.id=$1`, a.ID).Scan(&status, &encrypted); err != nil || status != "PENDING" || len(encrypted) == 0 {
					t.Fatal("execution failure mutated approval or staging", status, err)
				}
			}
			if users != 0 || requests != expected {
				t.Fatal("partial state on failed audit", users, requests)
			}
		})
	}
}
func TestAdminManagementApprovalExpiryDoesNotCreateAccountIntegration(t *testing.T) {
	f := newManagementFixture(t)
	in := f.input()
	a, err := f.create(t, in)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Add(approvalTTL)
	_, err = f.decide(t, 1, a.ID, true)
	nodeCode(t, err, httpx.ErrApprovalStale)
	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_users WHERE username=$1`, in.Username).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestAdminManagementApproverMustHoldEveryGrantedPermissionIntegration(t *testing.T) {
	f := newManagementFixture(t)
	in := f.input()
	a, err := f.create(t, in)
	if err != nil {
		t.Fatal(err)
	}
	var limited int64
	if err = f.owner.QueryRow(f.ctx, `INSERT INTO admin_roles(code,name) VALUES('limited_approver','Limited approver') RETURNING id`).Scan(&limited); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) SELECT $1,code FROM admin_permissions WHERE code <> 'audit.read'`, limited)
	if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_user_roles WHERE admin_user_id=$1`, f.people[1].ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, f.people[1].ID, limited)
	_, err = f.decide(t, 1, a.ID, true)
	nodeCode(t, err, httpx.ErrForbidden)
	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_users WHERE username=$1`, in.Username).Scan(&count); err != nil || count != 0 {
		t.Fatal("approver granted privilege they lack", count, err)
	}
}
