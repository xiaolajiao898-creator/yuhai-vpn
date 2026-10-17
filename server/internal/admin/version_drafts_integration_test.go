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

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func newVersionFixture(t *testing.T) (*approvalFixture, *VersionService) {
	t.Helper()
	f := newApprovalFixture(t)
	var before int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_role_permissions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../db/migrations/00014_admin_version_drafts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err = f.owner.Exec(f.ctx, up); err != nil {
		t.Fatal("actual migration", err)
	}
	var after, risks int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_role_permissions`).Scan(&after); err != nil || after != before {
		t.Fatal("migration granted roles", before, after, err)
	}
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_permissions WHERE (code='app_versions.read' AND risk_level=0) OR (code='app_versions.draft' AND risk_level=2)`).Scan(&risks); err != nil || risks != 2 {
		t.Fatal("permission definitions", risks, err)
	}
	if _, err = f.owner.Exec(f.ctx, "GRANT SELECT,INSERT,UPDATE ON admin_version_mutations TO "+pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	// Explicit fixture-only authorization, never part of the migration.
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'app_versions.read'),($1,'app_versions.draft')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, versionRead, versionDraft)
	}
	return f, &VersionService{Approvals: f.svc}
}
func versionProof(t *testing.T, f *approvalFixture, person int, method, path string, body any) (ApprovalProof, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	p := f.proof(t, person, versionDraft, path, string(raw))
	if method != "POST" {
		p.RequestHash = RequestHash(method, path, raw)
		f.exec(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), p.RequestHash)
	}
	return p, string(raw)
}
func createVersion(t *testing.T, f *approvalFixture, s *VersionService) VersionDetail {
	t.Helper()
	in := validDraftInput()
	p, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
	out, err := s.Save(f.ctx, p, 0, VersionUpdate{VersionCreate: in})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestVersionDraftCreateEditReplayAndPublishIntegration(t *testing.T) {
	f, s := newVersionFixture(t)
	in := validDraftInput()
	p, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
	out, err := s.Save(f.ctx, p, 0, VersionUpdate{VersionCreate: in})
	if err != nil || out.Status != "DRAFT" || !out.CanEdit || out.SnapshotHash == "" || out.PublishedAt != nil || out.ApprovalID != nil {
		t.Fatal("create", out, err)
	}
	id, _ := positiveApprovalID(out.ID)
	repeat, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
	replay, err := s.Save(f.ctx, repeat, 0, VersionUpdate{VersionCreate: in})
	if err != nil || replay.ID != out.ID {
		t.Fatal("idempotent replay", replay, err)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM app_versions`); n != 1 {
		t.Fatal("duplicate create", n)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_version_mutations`); n != 1 {
		t.Fatal("duplicate operation", n)
	}
	_, err = s.Save(f.ctx, repeat, 0, VersionUpdate{VersionCreate: in})
	approvalCode(t, err, httpx.ErrStepUpInvalid)
	query, err := s.List(f.ctx, f.people[0], VersionFilter{Platform: "ANDROID", VersionCode: "123"})
	if err != nil || query.Total != "1" || len(query.Items) != 1 || query.Items[0].ID != out.ID {
		t.Fatal("exact recovery query", query, err)
	}
	edit := VersionUpdate{VersionCreate: in, ExpectedSnapshotHash: out.SnapshotHash}
	edit.OperationID = uuid.NewString()
	edit.Notes = "更新后的公开发布说明"
	edit.VersionName = "1.2.4"
	update, _ := versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+out.ID, edit)
	changed, err := s.Save(f.ctx, update, id, edit)
	if err != nil || changed.SnapshotHash == out.SnapshotHash || changed.Notes != edit.Notes {
		t.Fatal("update", changed, err)
	}
	stale := edit
	stale.OperationID = uuid.NewString()
	proof, _ := versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+out.ID, stale)
	_, err = s.Save(f.ctx, proof, id, stale)
	approvalCode(t, err, httpx.ErrApprovalStale)
	f.unused(t, proof)
	in.VersionName = "different payload"
	conflict, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
	_, err = s.Save(f.ctx, conflict, 0, VersionUpdate{VersionCreate: in})
	approvalCode(t, err, httpx.ErrConflict)
	pending := f.request(t, id)
	detail, err := s.Get(f.ctx, f.people[0], id)
	if err != nil || detail.CanEdit || detail.PendingApprovalID == nil || *detail.PendingApprovalID != pending.ID {
		t.Fatal("pending edit lock", detail, err)
	}
	edit.ExpectedSnapshotHash = changed.SnapshotHash
	edit.OperationID = uuid.NewString()
	blocked, _ := versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+out.ID, edit)
	_, err = s.Save(f.ctx, blocked, id, edit)
	approvalCode(t, err, httpx.ErrConflict)
	f.unused(t, blocked)
	approvalID, _ := positiveApprovalID(pending.ID)
	decision := f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+pending.ID+"/approve", `{}`)
	if _, err = f.svc.Decide(f.ctx, decision, approvalID, true, ""); err != nil {
		t.Fatal("draft did not integrate with approval", err)
	}
	published, err := s.Get(f.ctx, f.people[0], id)
	if err != nil || published.Status != "PUBLISHED" || published.CanEdit {
		t.Fatal("published state", published, err)
	}
	edit.ExpectedSnapshotHash = published.SnapshotHash
	edit.OperationID = uuid.NewString()
	proof, _ = versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+out.ID, edit)
	_, err = s.Save(f.ctx, proof, id, edit)
	approvalCode(t, err, httpx.ErrConflict)
	var forbidden int
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM admin_audit_logs WHERE before_data::text LIKE '%download%' OR after_data::text LIKE '%download%' OR before_data::text LIKE '%release_notes%' OR after_data::text LIKE '%release_notes%'`).Scan(&forbidden); err != nil || forbidden != 0 {
		t.Fatal("public text copied to audit", forbidden, err)
	}
}
func TestVersionDraftConcurrentEditorsAndUniqueCreateIntegration(t *testing.T) {
	f, s := newVersionFixture(t)
	out := createVersion(t, f, s)
	id, _ := positiveApprovalID(out.ID)
	edits := make([]VersionUpdate, 2)
	proofs := make([]ApprovalProof, 2)
	for i := range edits {
		edits[i] = VersionUpdate{VersionCreate: validDraftInput(), ExpectedSnapshotHash: out.SnapshotHash}
		edits[i].Notes = fmt.Sprintf("editor %d", i)
		proofs[i], _ = versionProof(t, f, i, "PUT", "/admin/v1/app-versions/"+out.ID, edits[i])
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range edits {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = s.Save(f.ctx, proofs[i], id, edits[i]) }(i)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		} else {
			approvalCode(t, err, httpx.ErrApprovalStale)
		}
	}
	if success != 1 {
		t.Fatal("lost update", errs)
	}
	in := validDraftInput()
	dup, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
	_, err := s.Save(f.ctx, dup, 0, VersionUpdate{VersionCreate: in})
	approvalCode(t, err, httpx.ErrConflict)
	f.unused(t, dup)
	// Same moment and IDs above 9 must sort numerically, not lexicographically.
	for i := 0; i < 12; i++ {
		f.version(t)
	}
	list, err := s.List(f.ctx, f.people[0], VersionFilter{PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	// Older seeded drafts use the database clock, so compare adjacent equal timestamps.
	for i := 1; i < len(list.Items); i++ {
		a, b := list.Items[i-1], list.Items[i]
		if a.CreatedAt.Equal(b.CreatedAt) {
			x, _ := strconv.ParseInt(a.ID, 10, 64)
			y, _ := strconv.ParseInt(b.ID, 10, 64)
			if x < y {
				t.Fatal("lexical ID ordering")
			}
		}
	}
}
func TestVersionDraftAuditRollbackAndExpiryIntegration(t *testing.T) {
	for _, mode := range []string{"audit failure", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newVersionFixture(t)
			in := validDraftInput()
			proof, _ := versionProof(t, f, 0, "POST", "/admin/v1/app-versions", in)
			real := f.svc.Audit
			f.svc.Audit = authAuditFunc(func(ctx context.Context, tx pgx.Tx, e audit.Event) error {
				if err := real.Append(ctx, tx, e); err != nil {
					return err
				}
				if e.Result == "SUCCESS" {
					if mode == "expiry" {
						f.clock.Add(stepUpTTL)
					} else {
						return errors.New("audit failed with private payload")
					}
				}
				return nil
			})
			_, err := s.Save(f.ctx, proof, 0, VersionUpdate{VersionCreate: in})
			if err == nil {
				t.Fatal("unsafe save succeeded")
			}
			if mode == "expiry" {
				approvalCode(t, err, httpx.ErrStepUpInvalid)
			} else {
				approvalCode(t, err, httpx.ErrInternal)
			}
			f.unused(t, proof)
			for _, table := range []string{"app_versions", "admin_version_mutations"} {
				if n := authCount(t, f.svc.Auth, "SELECT count(*) FROM "+table); n != 0 {
					t.Fatal("partial mutation", table, n)
				}
			}
			if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_audit_logs WHERE result='SUCCESS'`); n != 0 {
				t.Fatal("uncommitted success audit")
			}
		})
	}
}
func TestVersionDraftHTTPAuthorizationAndPayloadBindingIntegration(t *testing.T) {
	f, s := newVersionFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: f.svc.Auth, SecureCookies: true}
	g := r.Group("/admin/v1")
	h.Register(g)
	(&VersionHandler{Auth: h, Service: s}).Register(g)
	send := func(method, path, body, token, csrf string, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://admin.example"+path, strings.NewReader(body))
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://admin.example")
		req.Header.Set("X-Step-Up-Token", token)
		req.Header.Set("X-CSRF-Token", csrf)
		if cookie {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	path := "/admin/v1/app-versions"
	in := validDraftInput()
	proof, body := versionProof(t, f, 0, "POST", path, in)
	csrf := csrfToken(f.sessions[0])
	if w := send("POST", path, body, proof.Token, csrf, false); w.Code != 401 {
		t.Fatal("missing cookie", w.Code)
	}
	if w := send("POST", path, body, proof.Token, "", true); w.Code != 403 {
		t.Fatal("missing csrf", w.Code)
	}
	if w := send("POST", path, body+" ", proof.Token, csrf, true); w.Code != 401 {
		t.Fatal("changed raw body accepted", w.Code)
	}
	f.unused(t, proof)
	good := send("POST", path, body, proof.Token, csrf, true)
	if good.Code != 201 {
		t.Fatal("create route", good.Code, good.Body.String())
	}
	var out VersionDetail
	if err := json.Unmarshal(good.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatal("bad route DTO", err)
	}
	if w := send("GET", path+"?platform=ANDROID&version_code=123", "", "", csrf, true); w.Code != 200 {
		t.Fatal("list route", w.Code)
	}
	edit := VersionUpdate{VersionCreate: validDraftInput(), ExpectedSnapshotHash: out.SnapshotHash}
	edit.Notes = "HTTP update"
	target := path + "/" + out.ID
	p, raw := versionProof(t, f, 0, "PUT", target, edit)
	if w := send("PUT", target, raw, p.Token, csrf, true); w.Code != 200 {
		t.Fatal("update route", w.Code, w.Body.String())
	}
	// Publish-action token cannot authorize a draft mutation.
	wrong := f.proof(t, 0, publishAction, target, raw)
	wrong.RequestHash = RequestHash("PUT", target, []byte(raw))
	f.exec(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(wrong.Token), wrong.RequestHash)
	if w := send("PUT", target, raw, wrong.Token, csrf, true); w.Code != 401 {
		t.Fatal("wrong token action", w.Code)
	}
	if _, err := f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='app_versions.draft'`, f.role); err != nil {
		t.Fatal(err)
	}
	fresh, freshRaw := versionProof(t, f, 0, "POST", path, validDraftInput())
	if w := send("POST", path, freshRaw, fresh.Token, csrf, true); w.Code != 403 {
		t.Fatal("revoked write permission", w.Code)
	}
	if _, err := f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='app_versions.read'`, f.role); err != nil {
		t.Fatal(err)
	}
	if w := send("GET", target, "", "", csrf, true); w.Code != 403 {
		t.Fatal("revoked read permission", w.Code)
	}
}

func TestVersionDraftPublicationRaceAndExpiredApprovalIntegration(t *testing.T) {
	f, s := newVersionFixture(t)
	original := createVersion(t, f, s)
	id, _ := positiveApprovalID(original.ID)
	edit := VersionUpdate{VersionCreate: validDraftInput(), ExpectedSnapshotHash: original.SnapshotHash}
	edit.Notes = "concurrent edit"
	editProof, _ := versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+original.ID, edit)
	publishProof := f.proof(t, 1, publishAction, "/admin/v1/app-versions/"+original.ID+"/publish", `{"reason":"发布核验"}`)
	var editErr, publishErr error
	var pending Approval
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, editErr = s.Save(f.ctx, editProof, id, edit) }()
	go func() {
		defer wg.Done()
		pending, publishErr = f.svc.RequestPublish(f.ctx, publishProof, id, "发布核验")
	}()
	wg.Wait()
	if publishErr != nil {
		t.Fatal("publish race", publishErr)
	}
	if editErr != nil {
		approvalCode(t, editErr, httpx.ErrConflict)
	}
	current, err := s.Get(f.ctx, f.people[0], id)
	if err != nil || current.Status != "DRAFT" || current.CanEdit || pending.PayloadHash != current.SnapshotHash {
		t.Fatal("pending approval diverged from locked draft", current, pending, err)
	}
	// Expired requests must not permanently lock a draft, and the later publish
	// request must retire the expired pending row rather than reuse its snapshot.
	f.clock.Add(approvalTTL)
	edit.ExpectedSnapshotHash = current.SnapshotHash
	edit.OperationID = uuid.NewString()
	edit.Notes = "edit after expired request"
	proof, _ := versionProof(t, f, 0, "PUT", "/admin/v1/app-versions/"+original.ID, edit)
	changed, err := s.Save(f.ctx, proof, id, edit)
	if err != nil || !changed.CanEdit || changed.PendingApprovalID != nil {
		t.Fatal("expired approval blocked edit", changed, err)
	}
	next := f.request(t, id)
	if next.ID == pending.ID || next.PayloadHash != changed.SnapshotHash {
		t.Fatal("expired approval reused", next)
	}
}
