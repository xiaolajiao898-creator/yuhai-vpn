package admin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/fieldenc"
)

type testClock struct{ unix atomic.Int64 }

func (c *testClock) Now() time.Time      { return time.Unix(c.unix.Load(), 0).UTC() }
func (c *testClock) Add(d time.Duration) { c.unix.Add(int64(d / time.Second)) }
func integrationService(t *testing.T) (*Service, *testClock, string, string, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	ownerURL := os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if url == "" || ownerURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated database migrated through 00012")
	}
	appConfig, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerURL)
	if err != nil {
		t.Fatal("invalid TEST_MIGRATE_DATABASE_URL")
	}
	if appConfig.ConnConfig.Host != ownerConfig.ConnConfig.Host || appConfig.ConnConfig.Port != ownerConfig.ConnConfig.Port || appConfig.ConnConfig.Database != ownerConfig.ConnConfig.Database || appConfig.ConnConfig.Port != 15432 {
		t.Fatal("test app and migration connections must target the same isolated database")
	}
	ctx := context.Background()
	base, err := pgxpool.NewWithConfig(ctx, ownerConfig.Copy())
	if err != nil {
		t.Fatal("cannot create fixture-owner connection")
	}
	t.Cleanup(base.Close)
	schema := "admin_auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	q := pgx.Identifier{schema}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+q+" CASCADE"); err != nil {
			t.Error("schema cleanup", err)
		}
	})
	for _, table := range []string{"admin_users", "admin_roles", "admin_permissions", "admin_role_permissions", "admin_user_roles", "admin_mfa_tickets", "admin_sessions", "admin_step_up_tokens", "admin_login_limits", "admin_audit_logs"} {
		if _, err = base.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA " + q + " TO " + pgx.Identifier{appConfig.ConnConfig.User}.Sanitize(),
		"GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + q + " TO " + pgx.Identifier{appConfig.ConnConfig.User}.Sanitize(),
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + q + " TO " + pgx.Identifier{appConfig.ConnConfig.User}.Sanitize(),
	} {
		if _, err = base.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema
	appConfig.ConnConfig.RuntimeParams["application_name"] = schema
	appConfig.MaxConns = 12
	ownerConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := fieldenc.New("admin-test", key)
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{}
	clk.unix.Store(2000000000)
	svc, err := New(pool, clk, enc)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 20)
	if _, err = rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	username := "test_" + hex.EncodeToString(raw[:10])
	password := "test password " + hex.EncodeToString(raw)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,totp_secret_enc) VALUES($1,$2,$3) RETURNING id`, username, hash, []byte{}).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := enc.Encrypt([]byte(secret), fieldenc.AAD("admin_users", "totp_secret_enc", id))
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE admin_users SET totp_secret_enc=$2 WHERE id=$1`, id, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ip := fmt.Sprintf("198.18.%d.%d", raw[0], raw[1])
	return svc, clk, username, password, secret + "|" + ip
}

// Every elevated follow-up remains inside the same random test schema.
func integrationOwner(t *testing.T, svc *Service) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_MIGRATE_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid fixture owner config")
	}
	schema := svc.Pool.Config().ConnConfig.RuntimeParams["search_path"]
	if !strings.HasPrefix(schema, "admin_auth_test_") || strings.ContainsAny(schema, ",\" ") {
		t.Fatal("owner requires an isolated auth schema")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	owner, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	return owner
}
func currentCode(secret string, now time.Time) string {
	raw, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	m := hmac.New(sha1.New, raw)
	_, _ = m.Write(counter[:])
	sum := m.Sum(nil)
	o := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[o:o+4])&0x7fffffff)%1000000)
}
func TestAdminConcurrentMFAAndSessionIntegration(t *testing.T) {
	svc, clk, username, password, fixture := integrationService(t)
	secret, ip, _ := strings.Cut(fixture, "|")
	ctx := context.Background()
	challenge, err := svc.Login(ctx, LoginInput{username, password}, ip)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	results := make([]SessionResult, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.MFA(ctx, MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, ip, "integration")
		}(i)
	}
	wg.Wait()
	successes := 0
	var session SessionResult
	for i, err := range errs {
		if err == nil {
			successes++
			session = results[i]
		}
	}
	if successes != 1 {
		t.Fatalf("single MFA challenge created %d sessions", successes)
	}
	var storedHash string
	var count int
	if err = svc.Pool.QueryRow(ctx, `SELECT token_hash FROM admin_sessions WHERE token_hash=$1`, tokenHash(session.Token)).Scan(&storedHash); err != nil || storedHash == session.Token {
		t.Fatal("session must store only hash", err)
	}
	if err = svc.Pool.QueryRow(ctx, `SELECT count(*) FROM admin_sessions s JOIN admin_users u ON u.id=s.admin_user_id WHERE u.username=$1`, username).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate session", count, err)
	}
	// A different password challenge cannot reuse the accepted TOTP step.
	challenge, err = svc.Login(ctx, LoginInput{username, password}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.MFA(ctx, MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, ip, "integration"); err == nil {
		t.Fatal("TOTP replay accepted on another challenge")
	}
	p, err := svc.Authenticate(ctx, session.Token, ip)
	if err != nil || p.Username != username || p.CSRFToken != session.CSRFToken {
		t.Fatal("session lookup", p, err)
	}
	if p.HasPermission("dev.tools") {
		t.Fatal("unassigned permission granted")
	}
	// CSRF rejection must happen before updating activity or executing a write.
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: svc}
	protected := r.Group("/admin/v1", h.Middleware())
	writes := 0
	protected.POST("/change", func(c *gin.Context) { writes++; c.Status(204) })
	request := func(csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://admin.example/admin/v1/change", bytes.NewBufferString("{}"))
		req.RemoteAddr = ip + ":1234"
		req.AddCookie(&http.Cookie{Name: CookieName, Value: session.Token})
		req.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := request(""); w.Code != 403 || writes != 0 {
		t.Fatal("missing CSRF accepted", w.Code)
	}
	if w := request(session.CSRFToken); w.Code != 204 || writes != 1 {
		t.Fatal("valid CSRF denied", w.Code, w.Body.String())
	}
	clk.Add(sessionIdle)
	if _, err = svc.Authenticate(ctx, session.Token, ip); err == nil {
		t.Fatal("idle session accepted")
	}
	clk.Add(-sessionIdle)
	if _, err = svc.Authenticate(ctx, session.Token, ip); err == nil {
		t.Fatal("revoked idle session resurrected")
	}
}
func TestAdminLockoutAndExpiredChallengeIntegration(t *testing.T) {
	svc, clk, username, password, fixture := integrationService(t)
	secret, ip, _ := strings.Cut(fixture, "|")
	ctx := context.Background()
	for i := 0; i < maxFailures; i++ {
		if _, err := svc.Login(ctx, LoginInput{username, "incorrect"}, ip); err == nil {
			t.Fatal("wrong password accepted")
		}
	}
	if _, err := svc.Login(ctx, LoginInput{username, password}, ip); err == nil {
		t.Fatal("locked user logged in")
	}
	var attempts int
	var status string
	if err := svc.Pool.QueryRow(ctx, `SELECT failed_attempts,status FROM admin_users WHERE username=$1`, username).Scan(&attempts, &status); err != nil || attempts != maxFailures || status != "LOCKED" {
		t.Fatal("failure count not durable", attempts, status, err)
	}
	clk.Add(lockTTL)
	challenge, err := svc.Login(ctx, LoginInput{username, password}, ip)
	if err != nil {
		t.Fatal("lock expiry", err)
	}
	clk.Add(ticketTTL)
	if _, err := svc.MFA(ctx, MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, ip, "integration"); err == nil {
		t.Fatal("expired challenge accepted")
	}
	challenge, err = svc.Login(ctx, LoginInput{username, password}, ip)
	if err != nil {
		t.Fatal(err)
	}
	session, err := svc.MFA(ctx, MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, ip, "integration")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Authenticate(ctx, session.Token, "203.0.113.9"); err == nil {
		t.Fatal("changed IP prefix accepted")
	}
	if _, err = svc.Authenticate(ctx, session.Token, ip); err == nil {
		t.Fatal("IP-revoked session reactivated")
	}
}

func TestAdminHTTPLoginLogoutIntegration(t *testing.T) {
	svc, clk, username, password, fixture := integrationService(t)
	secret, ip, _ := strings.Cut(fixture, "|")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{Service: svc, SecureCookies: true}
	handler.Register(router.Group("/admin/v1"))
	request := func(method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal("request encoding")
		}
		req := httptest.NewRequest(method, "https://admin.example/admin/v1"+path, bytes.NewReader(raw))
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://admin.example")
		req.Header.Set("X-CSRF-Token", csrf)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	login := request("POST", "/auth/login", LoginInput{username, password}, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("password login status %d", login.Code)
	}
	var challenge LoginResult
	if err := json.Unmarshal(login.Body.Bytes(), &challenge); err != nil || !validToken(challenge.MFATicket) {
		t.Fatal("missing password challenge")
	}
	if len(login.Result().Cookies()) != 0 {
		t.Fatal("password alone issued a session")
	}
	mfa := request("POST", "/auth/mfa", MFAInput{challenge.MFATicket, currentCode(secret, clk.Now())}, nil, "")
	if mfa.Code != http.StatusOK {
		t.Fatalf("MFA status %d", mfa.Code)
	}
	var session SessionResult
	if err := json.Unmarshal(mfa.Body.Bytes(), &session); err != nil || !validToken(session.CSRFToken) {
		t.Fatal("missing CSRF token")
	}
	cookies := mfa.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected exactly one session cookie")
	}
	cookie := cookies[0]
	if cookie.Name != CookieName || !validToken(cookie.Value) || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin/v1" || cookie.Domain != "" || cookie.MaxAge != int(sessionTTL.Seconds()) {
		t.Fatal("session cookie protection missing")
	}
	if strings.Contains(mfa.Body.String(), cookie.Value) {
		t.Fatal("bearer session token leaked in response JSON")
	}
	me := request("GET", "/auth/me", nil, cookie, "")
	if me.Code != http.StatusOK {
		t.Fatalf("me status %d", me.Code)
	}
	var principal Principal
	if err := json.Unmarshal(me.Body.Bytes(), &principal); err != nil || principal.Username != username || principal.ID == "" || principal.CSRFToken != session.CSRFToken || len(principal.Permissions) != 0 {
		t.Fatal("incorrect session principal")
	}
	denied := request("POST", "/auth/logout", nil, cookie, "")
	if denied.Code != http.StatusForbidden {
		t.Fatal("logout accepted missing CSRF")
	}
	if again := request("GET", "/auth/me", nil, cookie, ""); again.Code != http.StatusOK {
		t.Fatal("denied logout mutated the session")
	}
	logout := request("POST", "/auth/logout", nil, cookie, session.CSRFToken)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status %d", logout.Code)
	}
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 || cleared[0].Value != "" {
		t.Fatal("logout did not clear the cookie")
	}
	if again := request("GET", "/auth/me", nil, cookie, ""); again.Code != http.StatusUnauthorized {
		t.Fatal("logged-out bearer token still accepted")
	}
	malformed := request("POST", "/auth/login", map[string]string{"password": "sentinel-raw-body", "unknown": "sentinel-raw-body"}, nil, "")
	if malformed.Code != http.StatusBadRequest {
		t.Fatal("malformed login accepted")
	}
	if n := authCount(t, svc, `SELECT count(*) FROM admin_audit_logs WHERE result='SUCCESS'`); n != 3 {
		t.Fatal("HTTP auth successes not audited", n)
	}
	if n := authCount(t, svc, `SELECT count(*) FROM admin_audit_logs WHERE result='DENIED' AND admin_user_id IS NULL AND target_id IS NULL`); n != 2 {
		t.Fatal("guard denials missing or assigned an unverified identity", n)
	}
	rawAudit := authAuditRows(t, svc)
	for _, value := range []string{username, password, secret, challenge.MFATicket, cookie.Value, session.CSRFToken, "sentinel-raw-body"} {
		if strings.Contains(rawAudit, value) {
			t.Fatal("HTTP authentication secret copied into audit")
		}
	}
}
