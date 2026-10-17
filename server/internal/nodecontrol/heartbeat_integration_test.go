package nodecontrol

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type heartbeatFixture struct {
	ctx         context.Context
	pool, owner *pgxpool.Pool
	clock       *testClock
	ca          *testCA
	cert        tls.Certificate
	svc         *Service
}

func newHeartbeatFixture(t *testing.T) *heartbeatFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("isolated PostgreSQL test URLs required")
	}
	app, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid fixture application URL")
	}
	admin, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid fixture owner URL")
	}
	if (app.ConnConfig.Host != "127.0.0.1" && app.ConnConfig.Host != "localhost") || app.ConnConfig.Host != admin.ConnConfig.Host || app.ConnConfig.Port != 15432 || app.ConnConfig.Port != admin.ConnConfig.Port || app.ConnConfig.Database != admin.ConnConfig.Database || app.ConnConfig.User == admin.ConnConfig.User {
		t.Fatal("requires distinct roles on the isolated localhost:15432 database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.NewWithConfig(ctx, admin.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := "heartbeat_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	q := pgx.Identifier{schema}.Sanitize()
	role := pgx.Identifier{app.ConnConfig.User}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := base.Exec(c, "DROP SCHEMA "+q+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{"node_groups", "nodes"} {
		if _, err := base.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	admin.ConnConfig.RuntimeParams["search_path"] = schema
	owner, err := pgxpool.NewWithConfig(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	raw, err := os.ReadFile("../db/migrations/00018_node_heartbeats.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, strings.Split(string(raw), "-- +goose Down")[0]); err != nil {
		t.Fatal("actual migration failed", err)
	}
	raw, err = os.ReadFile("../db/migrations/00019_node_heartbeat_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, strings.Split(string(raw), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../db/migrations/00020_node_ready_protocols.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, strings.Split(string(raw), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"REVOKE ALL ON ALL TABLES IN SCHEMA " + q + " FROM " + role,
		"GRANT USAGE ON SCHEMA " + q + " TO " + role,
		"GRANT SELECT ON nodes TO " + role,
		"GRANT UPDATE(agent_version,online_count,load_score,last_heartbeat_at,updated_at) ON nodes TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON node_heartbeats TO " + role,
	} {
		if _, err := owner.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	app.ConnConfig.RuntimeParams["search_path"] = schema
	app.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	c := newClock()
	ca := newCA(t, c)
	cert := ca.issue(t, "spiffe://yuhai.internal/node/9007199254740993", nil)
	f := &heartbeatFixture{ctx, pool, owner, c, ca, cert, &Service{Pool: pool, Clock: c, Roots: ca.roots}}
	f.exec(t, `INSERT INTO node_groups(id,name) OVERRIDING SYSTEM VALUE VALUES(1,'fixture')`)
	hash := sha256.Sum256(cert.Certificate[0])
	pin := hex.EncodeToString(hash[:])
	f.exec(t, `INSERT INTO nodes(id,group_id,name,country_code,city,region,host,status,capacity_users,agent_cert_fingerprint,updated_at) OVERRIDING SYSTEM VALUE VALUES(9007199254740993,1,'fixture','HK','Hong Kong','ASIA','private-host.invalid','MAINTENANCE',100,$1,$2)`, pin, c.Now())
	return f
}
func (f *heartbeatFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func expectCode(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var e *httpx.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatal("unexpected error", err, "want", code)
	}
}
func (f *heartbeatFixture) accept(h Heartbeat) (Receipt, error) {
	return f.svc.Accept(f.ctx, f.ca.state(f.cert), h)
}

func TestHeartbeatDurableIdempotencyAndStateIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	h := heartbeatAt(f.clock, "1")
	out, err := f.accept(h)
	if err != nil || !out.Accepted || out.Duplicate || !out.ReceivedAt.Equal(f.clock.Now()) || out.Actions == nil || len(out.Actions) != 0 {
		t.Fatal(out, err)
	}
	var status, version string
	var online, load int
	var last time.Time
	var rx int64
	if err := f.owner.QueryRow(f.ctx, `SELECT n.status,n.agent_version,n.online_count,n.load_score,n.last_heartbeat_at,h.net_rx_bps FROM nodes n JOIN node_heartbeats h ON h.node_id=n.id`).Scan(&status, &version, &online, &load, &last, &rx); err != nil {
		t.Fatal(err)
	}
	if status != "MAINTENANCE" || version != "1.0.0" || online != 12 || load != 30 || rx != 9007199254740993 {
		t.Fatal("telemetry changed configuration or lost precision", status, version, online, load, rx)
	}
	f.clock.Add(2 * time.Minute)
	// Recreate the service to prove the receipt survives a process restart.
	f.svc = &Service{Pool: f.pool, Clock: f.clock, Roots: f.ca.roots}
	out, err = f.accept(h)
	if err != nil || !out.Duplicate || !out.ReceivedAt.Equal(last) {
		t.Fatal("durable duplicate", out, err)
	}
	if err := f.owner.QueryRow(f.ctx, `SELECT last_heartbeat_at FROM nodes`).Scan(&last); err != nil || !last.Equal(out.ReceivedAt) {
		t.Fatal("duplicate refreshed liveness", err)
	}
	h.OnlineCount++
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrConflict)
	h = heartbeatAt(f.clock, "2")
	h.OnlineCount = 501
	if out, err = f.accept(h); err != nil || out.Duplicate {
		t.Fatal(out, err)
	}
	if err := f.owner.QueryRow(f.ctx, `SELECT load_score,status FROM nodes`).Scan(&load, &status); err != nil || load != 100 || status != "MAINTENANCE" {
		t.Fatal("bounded load/state", err, load, status)
	}
	h = heartbeatAt(f.clock, "1")
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrConflict)
	h = heartbeatAt(f.clock, "3")
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrRateLimited)
	// A receipt never overrides a later administrator disable/revoke.
	f.exec(t, `UPDATE nodes SET status='DISABLED'`)
	_, err = f.accept(heartbeatAt(f.clock, "2"))
	expectCode(t, err, httpx.ErrForbidden)
	f.exec(t, `UPDATE nodes SET status='OFFLINE',agent_cert_fingerprint=NULL`)
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrForbidden)
	if _, err := f.pool.Exec(f.ctx, `UPDATE nodes SET status='ONLINE'`); err == nil {
		t.Fatal("runtime role can enable a node")
	}
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM node_heartbeats`); err == nil {
		t.Fatal("runtime role can erase replay watermark")
	}
}

func TestHeartbeatConcurrentAndRollbackIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	h := heartbeatAt(f.clock, "1")
	var wg sync.WaitGroup
	results := make(chan Receipt, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); out, err := f.accept(h); results <- out; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	accepted, duplicates := 0, 0
	for r := range results {
		if r.Duplicate {
			duplicates++
		} else {
			accepted++
		}
	}
	if accepted != 1 || duplicates != 7 {
		t.Fatal("non-idempotent concurrent update", accepted, duplicates)
	}
	var count int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM node_heartbeats`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	f.clock.Add(30 * time.Second)
	h = heartbeatAt(f.clock, "2")
	h.AgentVersion = "2.0.0"
	f.exec(t, `ALTER TABLE nodes ADD CONSTRAINT reject_fixture_version CHECK (agent_version IS DISTINCT FROM '2.0.0')`)
	if _, err := f.accept(h); err == nil {
		t.Fatal("fixture failure not exercised")
	}
	var sequence int64
	var agent string
	if err := f.owner.QueryRow(f.ctx, `SELECT h.sequence,n.agent_version FROM node_heartbeats h JOIN nodes n ON n.id=h.node_id`).Scan(&sequence, &agent); err != nil || sequence != 1 || agent != "1.0.0" {
		t.Fatal("receipt partially committed", sequence, agent, err)
	}
	f.exec(t, `ALTER TABLE nodes DROP CONSTRAINT reject_fixture_version`)
	if _, err := f.accept(h); err != nil {
		t.Fatal("retry of rolled back sequence", err)
	}
	// Row lock held by configuration changes: caller cancellation cannot commit.
	lock, err := f.owner.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err := lock.Exec(f.ctx, `UPDATE nodes SET status='DISABLED'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer cancel()
	f.clock.Add(30 * time.Second)
	if _, err := f.svc.Accept(ctx, f.ca.state(f.cert), heartbeatAt(f.clock, "3")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("blocked request did not respect deadline", err)
	}
	if err := lock.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrForbidden)
	if err := f.owner.QueryRow(f.ctx, `SELECT sequence FROM node_heartbeats`).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatal("cancelled request wrote data", err)
	}
}

type heartbeatQueryHook struct {
	prefix string
	after  func()
	once   sync.Once
}
type heartbeatHookKey struct{}

func (h *heartbeatQueryHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, heartbeatHookKey{}, strings.HasPrefix(d.SQL, h.prefix))
}
func (h *heartbeatQueryHook) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if match, _ := ctx.Value(heartbeatHookKey{}).(bool); match && d.Err == nil {
		h.once.Do(h.after)
	}
}

func TestHeartbeatTimeWindowsAndAuthorityRecheckIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	for _, offset := range []time.Duration{-60*time.Second - time.Microsecond, 5*time.Second + time.Microsecond} {
		h := heartbeatAt(f.clock, "1")
		h.SentAt = f.clock.Now().Add(offset).Format(time.RFC3339Nano)
		_, err := f.accept(h)
		expectCode(t, err, httpx.ErrTimestampSkew)
	}
	h := heartbeatAt(f.clock, "1")
	h.SentAt = f.clock.Now().Add(5 * time.Second).Format(time.RFC3339Nano)
	if _, err := f.accept(h); err != nil {
		t.Fatal("upper bound", err)
	}
	f.clock.Add(30 * time.Second)
	h = heartbeatAt(f.clock, "2")
	h.SentAt = f.clock.Now().Add(-60 * time.Second).Format(time.RFC3339Nano)
	_, err := f.accept(h)
	expectCode(t, err, httpx.ErrConflict)
	h = heartbeatAt(f.clock, "2")
	if _, err := f.accept(h); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(-time.Second)
	h = heartbeatAt(f.clock, "3")
	_, err = f.accept(h)
	expectCode(t, err, httpx.ErrConflict)
	for _, prefix := range []string{"SELECT status,agent_cert_fingerprint", "UPDATE nodes SET agent_version"} {
		t.Run(prefix, func(t *testing.T) {
			f := newHeartbeatFixture(t)
			cfg := f.pool.Config().Copy()
			cfg.ConnConfig.Tracer = &heartbeatQueryHook{prefix: prefix, after: func() { f.clock.Add(2 * time.Hour) }}
			pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			f.svc = &Service{Pool: pool, Clock: f.clock, Roots: f.ca.roots}
			_, err = f.accept(heartbeatAt(f.clock, "1"))
			expectCode(t, err, httpx.ErrForbidden)
			var n int
			if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM node_heartbeats`).Scan(&n); err != nil || n != 0 {
				t.Fatal("expired certificate committed receipt", n, err)
			}
		})
	}
}

func TestHeartbeatActualMTLSHTTPIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	cert, key, root := f.ca.serverFiles(t)
	srv, err := NewServer("127.0.0.1:0", cert, key, root, f.pool, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.TLS = srv.TLSConfig
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.StartTLS()
	defer ts.Close()
	client := func(cert *tls.Certificate, maxVersion uint16) *http.Client {
		cfg := &tls.Config{RootCAs: f.ca.roots, MinVersion: tls.VersionTLS12, MaxVersion: maxVersion, Time: f.clock.Now}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		transport := &http.Transport{TLSClientConfig: cfg}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	send := func(c *http.Client, path, body string) (int, []byte, error) {
		r, err := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Node-Id", "999")
		r.Header.Set("X-Forwarded-Client-Cert", "untrusted")
		resp, err := c.Do(r)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if resp.Header.Get("Cache-Control") != "no-store" && resp.StatusCode != 404 {
			t.Fatal("cache policy missing")
		}
		return resp.StatusCode, b, err
	}
	h := heartbeatAt(f.clock, "1")
	raw, _ := json.Marshal(h)
	valid := client(&f.cert, tls.VersionTLS13)
	status, body, err := send(valid, "/node/v1/heartbeat", string(raw))
	if err != nil || status != 200 {
		t.Fatal("real TLS heartbeat failed", status, string(body), err)
	}
	var response struct {
		Data      Receipt `json:"data"`
		RequestID string  `json:"request_id"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.RequestID == "" || !response.Data.Accepted || response.Data.Sequence != "1" {
		t.Fatal("contract mismatch", string(body), err)
	}
	for _, secret := range []string{"private-host", "fingerprint", "BEGIN CERTIFICATE", "node_id"} {
		if strings.Contains(string(body), secret) {
			t.Fatal("sensitive response", secret)
		}
	}
	for _, c := range []*http.Client{client(nil, tls.VersionTLS13), client(&f.cert, tls.VersionTLS12), client(ptrCert(newCA(t, f.clock).issue(t, "spiffe://yuhai.internal/node/9007199254740993", nil)), tls.VersionTLS13)} {
		if _, _, err := send(c, "/node/v1/heartbeat", string(raw)); err == nil {
			t.Fatal("TLS handshake accepted missing/untrusted/old credentials")
		}
	}
	for _, cert := range []tls.Certificate{f.ca.issue(t, "spiffe://yuhai.internal/node/9007199254740993", nil), f.ca.issue(t, "spiffe://yuhai.internal/node/7", nil), f.ca.issue(t, "", nil)} {
		status, _, err := send(client(&cert, tls.VersionTLS13), "/node/v1/heartbeat", string(raw))
		if err != nil || status != 403 {
			t.Fatal("unpinned/unknown/non-SAN certificate authorized", status, err)
		}
	}
	status, _, err = send(valid, "/node/v1/heartbeat?node_id=7", string(raw))
	if err != nil || status != 400 {
		t.Fatal("query accepted", status, err)
	}
	status, _, err = send(valid, "/node/v1/enroll", `{}`)
	if err != nil || status != 404 {
		t.Fatal("unimplemented enrollment exposed", status, err)
	}
	// A non-TLS request with forged proxy headers must fail before the DB.
	r := httptest.NewRequest("POST", "http://node.test/node/v1/heartbeat", strings.NewReader(string(raw)))
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Node-Id", "9007199254740993")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("proxy identity accepted", w.Code)
	}
	f.exec(t, `UPDATE nodes SET agent_cert_fingerprint=$1`, strings.Repeat("a", 64))
	status, _, err = send(valid, "/node/v1/heartbeat", string(raw))
	if err != nil || status != 403 {
		t.Fatal("existing TLS session bypassed live pin revocation", status, err)
	}
	// Restore the test pin, then advance time past leaf expiry on same keepalive.
	digest := sha256.Sum256(f.cert.Certificate[0])
	f.exec(t, `UPDATE nodes SET agent_cert_fingerprint=$1`, hex.EncodeToString(digest[:]))
	f.clock.Add(2 * time.Hour)
	status, _, err = send(valid, "/node/v1/heartbeat", string(raw))
	if err != nil || status != 403 {
		t.Fatal("expired keepalive authorized", status)
	}
}

func ptrCert(c tls.Certificate) *tls.Certificate { return &c }

func TestHeartbeatSequenceOrderAndMigrationDownIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	// Gaps are permitted, but lower sequences can never overwrite latest metrics.
	for _, seq := range []int64{1, 9007199254740993, 9223372036854775807} {
		h := heartbeatAt(f.clock, strconv.FormatInt(seq, 10))
		if _, err := f.accept(h); err != nil {
			t.Fatal(seq, err)
		}
		f.clock.Add(30 * time.Second)
	}
	_, err := f.accept(heartbeatAt(f.clock, "2"))
	expectCode(t, err, httpx.ErrConflict)
	raw, err := os.ReadFile("../db/migrations/00018_node_heartbeats.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, strings.Split(string(raw), "-- +goose Down")[1])
	var status string
	if err := f.owner.QueryRow(f.ctx, `SELECT status FROM nodes`).Scan(&status); err != nil || status != "MAINTENANCE" {
		t.Fatal("down migration modified node", err)
	}
	// This schema is disposable. Never roll down a live replay watermark table.
}
