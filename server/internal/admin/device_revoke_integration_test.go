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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type revokeFixture struct {
	*managedUserFixture
	revoker                *DeviceRevokeService
	deviceID, credentialID int64
}

func newRevokeFixture(t *testing.T) *revokeFixture {
	t.Helper()
	f := &revokeFixture{managedUserFixture: newManagedUserFixture(t)}
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"device_credentials", "auth_sessions", "node_user_events", "device_bind_logs"} {
		if _, err := f.owner.Exec(f.ctx, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00016_admin_device_revoke.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err = f.owner.Exec(f.ctx, up); err != nil {
		t.Fatal("actual migration", err)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("migration granted production capabilities")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='devices.revoke' AND risk_level=2`) != 1 {
		t.Fatal("permission not defined")
	}
	for _, table := range []string{"users", "devices", "device_credentials", "auth_sessions", "node_user_events", "device_bind_logs", "admin_device_mutations"} {
		if _, err = f.owner.Exec(f.ctx, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{table}.Sanitize()+" TO "+role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.owner.Exec(f.ctx, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'devices.revoke')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, deviceRevokeAction)
	}
	if err = f.pool.QueryRow(f.ctx, `SELECT id FROM devices WHERE user_id=$1 AND status='ACTIVE' ORDER BY id LIMIT 1`, f.userID).Scan(&f.deviceID); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO device_credentials(device_id,proxy_uuid,proxy_secret_enc) VALUES($1,$2,'private-credential-sentinel') RETURNING id`, f.deviceID, uuid.NewString()).Scan(&f.credentialID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO auth_sessions(user_id,device_id,refresh_hash,family_id,secret_version,expires_at) VALUES($1,$2,$3,$4,1,$5)`, f.userID, f.deviceID, strings.Repeat("c", 64), uuid.NewString(), f.clock.Now().Add(time.Hour))
	f.revoker = &DeviceRevokeService{Security: f.svc}
	return f
}
func (f *revokeFixture) input() DeviceRevokeInput {
	return DeviceRevokeInput{OperationID: uuid.NewString(), UserID: strconv.FormatInt(f.userID, 10), Reason: "设备遗失，撤销访问"}
}
func (f *revokeFixture) revokeProof(t *testing.T, person int, id int64, in DeviceRevokeInput) (ApprovalProof, string) {
	t.Helper()
	raw, _ := json.Marshal(in)
	path := fmt.Sprintf("/admin/v1/devices/%d", id)
	p := f.proof(t, person, deviceRevokeAction, path, string(raw))
	p.RequestHash = RequestHash("DELETE", path, raw)
	f.exec(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), p.RequestHash)
	return p, string(raw)
}
func (f *revokeFixture) assertState(t *testing.T, status string, events, operations, successAudits int) {
	t.Helper()
	var device, credential string
	var liveSessions, nodeEvents, mutations, audits int
	err := f.pool.QueryRow(f.ctx, `SELECT d.status,c.status,(SELECT count(*) FROM auth_sessions WHERE device_id=d.id AND revoked_at IS NULL),(SELECT count(*) FROM node_user_events WHERE credential_id=c.id),(SELECT count(*) FROM admin_device_mutations),(SELECT count(*) FROM admin_audit_logs WHERE action_code='devices.revoke' AND result='SUCCESS') FROM devices d JOIN device_credentials c ON c.device_id=d.id WHERE d.id=$1`, f.deviceID).Scan(&device, &credential, &liveSessions, &nodeEvents, &mutations, &audits)
	if err != nil || device != status || credential != status || nodeEvents != events || mutations != operations || audits != successAudits {
		t.Fatal("revocation state", device, credential, liveSessions, nodeEvents, mutations, audits, err)
	}
	if status == "REVOKED" && liveSessions != 0 {
		t.Fatal("refresh session survived", liveSessions)
	}
}
func TestAdminDeviceRevokeReplayConcurrencyAndProjectionIntegration(t *testing.T) {
	f := newRevokeFixture(t)
	in := f.input()
	p, _ := f.revokeProof(t, 0, f.deviceID, in)
	const n = 6
	outs := make([]DeviceRevokeResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() { defer wg.Done(); outs[i], errs[i] = f.revoker.Revoke(f.ctx, p, f.deviceID, in) }()
	}
	wg.Wait()
	first, _ := json.Marshal(outs[0])
	for i, err := range errs {
		raw, _ := json.Marshal(outs[i])
		if err != nil || string(raw) != string(first) {
			t.Fatal("exact concurrent replay", i, err, string(raw), string(first))
		}
	}
	if outs[0].AlreadyRevoked || outs[0].NodeSync != "QUEUED" || outs[0].Device.Status != "REVOKED" || outs[0].Device.RevokedAt == nil {
		t.Fatal(outs[0])
	}
	f.assertState(t, "REVOKED", 1, 1, 1)
	fresh, _ := f.revokeProof(t, 0, f.deviceID, in)
	if _, err := f.revoker.Revoke(f.ctx, fresh, f.deviceID, in); err != nil {
		t.Fatal("fresh proof replay", err)
	}
	f.assertState(t, "REVOKED", 1, 1, 1)
	changed := in
	changed.Reason = "不同理由"
	conflict, _ := f.revokeProof(t, 0, f.deviceID, changed)
	_, err := f.revoker.Revoke(f.ctx, conflict, f.deviceID, changed)
	approvalCode(t, err, httpx.ErrConflict)
	f.unused(t, conflict)
	wrong := p
	wrong.RequestHash = RequestHash("DELETE", "/admin/v1/devices/999", nil)
	_, err = f.revoker.Revoke(f.ctx, wrong, 999, in)
	approvalCode(t, err, httpx.ErrConflict)
	detail, err := f.service.Device(f.ctx, f.people[0], f.userID, f.deviceID)
	if err != nil || detail.Device.Status != "REVOKED" {
		t.Fatal("exact recovery", detail, err)
	}
	_, err = f.service.Device(f.ctx, f.people[0], f.emptyID, f.deviceID)
	approvalCode(t, err, httpx.ErrNotFound)
	for _, value := range []any{outs[0], detail} {
		raw, _ := json.Marshal(value)
		for _, secret := range []string{"private-", "proxy", "pubkey", "fingerprint", "token", "hash", "secret"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("private fields leaked", secret)
			}
		}
	}
	var before, after string
	if err = f.pool.QueryRow(f.ctx, `SELECT before_data::text,after_data::text FROM admin_audit_logs WHERE action_code='devices.revoke' AND result='SUCCESS'`).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(before+after, "credential") || !strings.Contains(after, in.Reason) {
		t.Fatal("audit projection or reason missing")
	}
}
func TestAdminDeviceRevokeHistoricalRepairAndDistinctActorsIntegration(t *testing.T) {
	f := newRevokeFixture(t)
	f.exec(t, `UPDATE devices SET status='REVOKED',revoked_at=NULL WHERE id=$1`, f.deviceID)
	in := f.input()
	p, _ := f.revokeProof(t, 0, f.deviceID, in)
	out, err := f.revoker.Revoke(f.ctx, p, f.deviceID, in)
	if err != nil || !out.AlreadyRevoked || out.Device.RevokedAt != nil || out.NodeSync != "QUEUED" {
		t.Fatal("legacy cleanup", out, err)
	}
	f.assertState(t, "REVOKED", 1, 1, 1)
	other := f.input()
	proof, _ := f.revokeProof(t, 1, f.deviceID, other)
	out, err = f.revoker.Revoke(f.ctx, proof, f.deviceID, other)
	if err != nil || !out.AlreadyRevoked || out.NodeSync != "NOT_REQUIRED" || out.Device.RevokedAt != nil {
		t.Fatal("repeat desired state", out, err)
	}
	f.assertState(t, "REVOKED", 1, 2, 2)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM device_bind_logs WHERE action='REVOKE_BY_ADMIN'`) != 1 {
		t.Fatal("duplicate bind log")
	}
}
func TestAdminDeviceRevokeAuthorizationBindingAndAtomicRollbackIntegration(t *testing.T) {
	for _, mode := range []string{"no permission", "other user", "other device", "changed bytes", "expired proof", "expired session", "revoked session", "disabled admin", "node event failure", "audit failure", "expiry after audit"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevokeFixture(t)
			in := f.input()
			p, _ := f.revokeProof(t, 0, f.deviceID, in)
			id := f.deviceID
			want := httpx.ErrStepUpInvalid
			switch mode {
			case "no permission":
				f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='devices.revoke'`, f.role)
				id = 999999
				want = httpx.ErrForbidden
			case "other user":
				in.UserID = strconv.FormatInt(f.emptyID, 10)
				p, _ = f.revokeProof(t, 0, id, in)
				want = httpx.ErrNotFound
			case "other device":
				id = 999999
			case "changed bytes":
				p.RequestHash = RequestHash("DELETE", fmt.Sprintf("/admin/v1/devices/%d", id), []byte("{}"))
			case "expired proof":
				f.exec(t, `UPDATE admin_step_up_tokens SET expires_at=$2 WHERE token_hash=$1`, tokenHash(p.Token), f.clock.Now())
			case "expired session":
				f.exec(t, `UPDATE admin_sessions SET expires_at=$2 WHERE id=$1`, p.Principal.SessionID, f.clock.Now())
			case "revoked session":
				f.exec(t, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, p.Principal.SessionID, f.clock.Now())
			case "disabled admin":
				f.exec(t, `UPDATE admin_users SET status='DISABLED' WHERE id=$1`, p.Principal.ID)
			case "node event failure":
				_, e := f.owner.Exec(f.ctx, `ALTER TABLE node_user_events ADD CONSTRAINT reject_fixture CHECK(effective_tier<>0)`)
				if e != nil {
					t.Fatal(e)
				}
				want = httpx.ErrInternal
			case "audit failure":
				f.svc.Audit = approvalAuditFunc(func(ctx context.Context, tx pgx.Tx, e audit.Event) error {
					if e.Result == "SUCCESS" {
						if err := f.audit.Append(ctx, tx, e); err != nil {
							return err
						}
						return errors.New("private database failure")
					}
					return f.audit.Append(ctx, tx, e)
				})
				want = httpx.ErrInternal
			case "expiry after audit":
				f.svc.Audit = approvalAuditFunc(func(ctx context.Context, tx pgx.Tx, e audit.Event) error {
					err := f.audit.Append(ctx, tx, e)
					if e.Result == "SUCCESS" {
						f.clock.unix.Add(int64(stepUpTTL.Seconds()))
					}
					return err
				})
			}
			// The service receives only a server-calculated hash. Reproduce the
			// handler's concrete URI change rather than trusting a stale hash.
			if mode == "other device" {
				raw, _ := json.Marshal(in)
				p.RequestHash = RequestHash("DELETE", fmt.Sprintf("/admin/v1/devices/%d", id), raw)
			}
			out, err := f.revoker.Revoke(f.ctx, p, id, in)
			approvalCode(t, err, want)
			if out.Device.ID != "" {
				t.Fatal("failure returned device")
			}
			f.assertState(t, "ACTIVE", 0, 0, 0)
			f.unused(t, p)
		})
	}
}

func (f *revokeFixture) waitBlocked(t *testing.T, pid int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := f.owner.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND $2=ANY(pg_blocking_pids(pid)))`, f.schema, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("did not reach lock")
		case <-ticker.C:
		}
	}
}
func TestAdminDeviceRevokeWaitsForRefreshAndRejectsLateAuthorityIntegration(t *testing.T) {
	for _, mode := range []string{"refresh descendant", "expiry while waiting", "permission while waiting"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevokeFixture(t)
			in := f.input()
			p, _ := f.revokeProof(t, 0, f.deviceID, in)
			tx, err := f.owner.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackApproval(tx)
			var pid int32
			if err = tx.QueryRow(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if mode == "refresh descendant" {
				if _, err = tx.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "refresh-device:"+strconv.FormatInt(f.deviceID, 10)); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(f.ctx, `UPDATE auth_sessions SET rotated_at=$2 WHERE device_id=$1`, f.deviceID, f.clock.Now()); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(f.ctx, `INSERT INTO auth_sessions(user_id,device_id,refresh_hash,family_id,secret_version,expires_at) VALUES($1,$2,$3,$4,1,$5)`, f.userID, f.deviceID, strings.Repeat("d", 64), uuid.NewString(), f.clock.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			} else if mode == "expiry while waiting" {
				if _, err = tx.Exec(f.ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, f.userID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = tx.Exec(f.ctx, `SELECT id FROM admin_users WHERE id=$1 FOR UPDATE`, p.Principal.ID); err != nil {
					t.Fatal(err)
				}
			}
			result := make(chan error, 1)
			go func() { _, e := f.revoker.Revoke(f.ctx, p, f.deviceID, in); result <- e }()
			f.waitBlocked(t, pid)
			if mode == "expiry while waiting" {
				f.clock.unix.Add(int64(stepUpTTL.Seconds()))
			}
			if mode == "permission while waiting" {
				if _, err = tx.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='devices.revoke'`, f.role); err != nil {
					t.Fatal(err)
				}
			}
			if err = tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = <-result
			if mode == "refresh descendant" {
				if err != nil {
					t.Fatal(err)
				}
				f.assertState(t, "REVOKED", 1, 1, 1)
			} else {
				if mode == "expiry while waiting" {
					approvalCode(t, err, httpx.ErrStepUpInvalid)
				} else {
					approvalCode(t, err, httpx.ErrForbidden)
				}
				f.assertState(t, "ACTIVE", 0, 0, 0)
				f.unused(t, p)
			}
		})
	}
}
func TestAdminDeviceRevokeHTTPAndExactReadIntegration(t *testing.T) {
	f := newRevokeFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(httpx.RequestIDMiddleware())
	auth := &Handler{Service: f.svc.Auth, SecureCookies: true}
	g := r.Group("/admin/v1")
	auth.Register(g)
	(&DeviceRevokeHandler{Auth: auth, Service: f.revoker}).Register(g)
	(&UserDevicesHandler{Auth: auth, Service: f.service}).Register(g)
	request := func(method, path, body, token string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://admin.example"+path, strings.NewReader(body))
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://admin.example")
		req.Header.Set("X-Step-Up-Token", token)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
			req.Header.Set("X-CSRF-Token", csrfToken(f.sessions[0]))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	in := f.input()
	p, raw := f.revokeProof(t, 0, f.deviceID, in)
	path := fmt.Sprintf("/admin/v1/devices/%d", f.deviceID)
	if w := request("DELETE", path, raw, p.Token, false); w.Code != 401 {
		t.Fatal("unauthenticated", w.Code, w.Body.String())
	}
	if w := request("DELETE", path, " "+raw, p.Token, true); w.Code != 401 {
		t.Fatal("body mutation escaped binding", w.Code, w.Body.String())
	}
	f.unused(t, p)
	for _, bad := range []string{`{"operation_id":"` + in.OperationID + `","user_id":"1","reason":"a","reason":"b"}`, `{"operation_id":"` + in.OperationID + `","user_id":null,"reason":"a"}`, raw[:len(raw)-1] + `,"unexpected":"x"}`} {
		if w := request("DELETE", path, bad, p.Token, true); w.Code != 400 {
			t.Fatal("malformed input", w.Code, w.Body.String())
		}
	}
	if w := request("DELETE", path, raw, p.Token, true); w.Code != 200 {
		t.Fatal("write", w.Code, w.Body.String())
	}
	f.assertState(t, "REVOKED", 1, 1, 1)
	get := fmt.Sprintf("/admin/v1/users/%d/devices/%d", f.userID, f.deviceID)
	w := request("GET", get, "", "", true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("read", w.Code, w.Body.String())
	}
	var detail ManagedDeviceDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.Device.Status != "REVOKED" || detail.UserID != in.UserID {
		t.Fatal("get shape", detail, err)
	}
	if w = request("GET", fmt.Sprintf("/admin/v1/users/%d/devices/%d", f.emptyID, f.deviceID), "", "", true); w.Code != 404 {
		t.Fatal("foreign user device", w.Code)
	}
	if w = request("GET", get+"?user_id=5", "", "", true); w.Code != 400 {
		t.Fatal("extra parameters", w.Code)
	}
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
	if w = request("GET", get, "", "", true); w.Code != 403 {
		t.Fatal("live read permission", w.Code, w.Body.String())
	}
}

func TestAdminDeviceRevokeDifferentAdministratorsAndNoCredentialIntegration(t *testing.T) {
	f := newRevokeFixture(t)
	inputs := []DeviceRevokeInput{f.input(), f.input()}
	proofs := make([]ApprovalProof, 2)
	for i := range proofs {
		proofs[i], _ = f.revokeProof(t, i, f.deviceID, inputs[i])
	}
	var wg sync.WaitGroup
	out := make([]DeviceRevokeResult, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); out[i], errs[i] = f.revoker.Revoke(f.ctx, proofs[i], f.deviceID, inputs[i]) }()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if out[0].AlreadyRevoked == out[1].AlreadyRevoked {
		t.Fatal("device transition was not serialized")
	}
	f.assertState(t, "REVOKED", 1, 2, 2)
	var other int64
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM devices WHERE user_id=$1 AND status='ACTIVE' LIMIT 1`, f.userID).Scan(&other); err != nil {
		t.Fatal(err)
	}
	in := f.input()
	p, _ := f.revokeProof(t, 0, other, in)
	result, err := f.revoker.Revoke(f.ctx, p, other, in)
	if err != nil || result.AlreadyRevoked || result.NodeSync != "NOT_REQUIRED" || result.Device.RevokedAt == nil {
		t.Fatal("credential-free device", result, err)
	}
}

type exactDeviceReadHook struct{ after func() }
type exactDeviceReadKey struct{}

func (h *exactDeviceReadHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, exactDeviceReadKey{}, strings.HasPrefix(data.SQL, "SELECT "+managedDeviceColumns+" FROM devices d WHERE"))
}
func (h *exactDeviceReadHook) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if hit, _ := ctx.Value(exactDeviceReadKey{}).(bool); hit && data.Err == nil {
		h.after()
	}
}

func TestAdminExactDeviceReadRechecksAuthorizationIntegration(t *testing.T) {
	for _, mode := range []string{"permission", "session", "expiry", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newRevokeFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			hits := 0
			cfg := f.pool.Config().Copy()
			cfg.ConnConfig.Tracer = &exactDeviceReadHook{after: func() {
				hits++
				switch mode {
				case "permission":
					f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
				case "session":
					f.write(t, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, f.people[0].SessionID, f.clock.Now())
				case "expiry":
					f.clock.Add(sessionTTL)
				case "cancel":
					cancel()
				}
			}}
			pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			auth := *f.svc.Auth
			auth.Pool = pool
			out, err := (&UserReadService{Auth: &auth}).Device(ctx, f.people[0], f.userID, f.deviceID)
			if err == nil || out.Device.ID != "" || hits != 1 {
				t.Fatal("late unauthorized result returned", out, err, hits)
			}
		})
	}
}
