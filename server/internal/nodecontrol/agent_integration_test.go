package nodecontrol

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/node"
	"github.com/yuhai-vpn/server/internal/nodeagent"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type agentSample struct {
	f     *heartbeatFixture
	calls int
}

func (s *agentSample) Sample(context.Context) (nodewire.Heartbeat, error) {
	s.calls++
	h := heartbeatAt(s.f.clock, "1")
	h.ConfigVersion = "1"
	h.ReadyProtocols = []string{"WIREGUARD"}
	h.SnapshotChecksum = strings.Repeat("b", 64)
	return h, nil
}

func TestAgentRestartActualTLSAndCandidateAvailabilityIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	pub, key, root := f.ca.serverFiles(t)
	srv, err := NewServer("127.0.0.1:0", pub, key, root, f.pool, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	mode.Store(1)
	var mu sync.Mutex
	var bodies [][]byte
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if mode.Load() == 2 {
			http.Redirect(w, r, "https://unexpected.invalid/", http.StatusTemporaryRedirect)
			return
		}
		if mode.Load() == 1 {
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Errorf("drop fixture failed: %d", rec.Code)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		srv.Handler.ServeHTTP(w, r)
	}))
	ts.TLS = srv.TLSConfig
	ts.StartTLS()
	defer ts.Close()
	dir := t.TempDir()
	clientCert := filepath.Join(dir, "node.crt")
	clientKey := filepath.Join(dir, "node.key")
	keyBytes, err := x509.MarshalPKCS8PrivateKey(f.cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := nodeagent.NewClient(ts.URL, clientCert, clientKey, root, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	stateDir := filepath.Join(dir, "state")
	if err := nodeagent.InitStore(stateDir, client.NodeID(), client.URL()); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenStore(stateDir, client.NodeID(), client.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sampler := &agentSample{f: f}
	runner := &nodeagent.Runner{Store: store, Sender: client, Sampler: sampler, Clock: f.clock}
	result, err := runner.Step(f.ctx)
	if err != nil || result.State != "pending" {
		t.Fatal("lost response not preserved", result, err)
	}
	store.Close()
	client.Close()
	mode.Store(0)
	store, err = nodeagent.OpenStore(stateDir, client.NodeID(), client.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner.Store = store
	f.clock.Add(2 * time.Minute)
	result, err = runner.Step(f.ctx)
	if err != nil || result.State != "accepted" || sampler.calls != 1 {
		t.Fatal("restart did not replay exact request", result, err, sampler.calls)
	}
	mu.Lock()
	same := len(bodies) == 2 && bytes.Equal(bodies[0], bodies[1])
	mu.Unlock()
	if !same {
		t.Fatal("uncertain request changed")
	}
	var received time.Time
	var seq int64
	var pin string
	if err := f.owner.QueryRow(f.ctx, `SELECT sequence,received_at,cert_fingerprint FROM node_heartbeats`).Scan(&seq, &received, &pin); err != nil || seq != 1 || f.clock.Now().Sub(received) != 2*time.Minute || len(pin) != 64 {
		t.Fatal("replay refreshed receipt", seq, received, err)
	}
	// Install read-only App fixtures in the same disposable schema; no production writes.
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"users", "subscriptions", "traffic_packs", "node_inbounds"} {
		f.exec(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
		f.exec(t, "GRANT SELECT ON "+pgx.Identifier{table}.Sanitize()+" TO "+role)
	}
	f.exec(t, "GRANT SELECT ON node_groups TO "+role)
	f.exec(t, `INSERT INTO users(id,account_no,secret_lookup,invite_code) OVERRIDING SYSTEM VALUE VALUES(1,'8600000001',$1,'TESTCODE')`, strings.Repeat("1", 64))
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,expire_at) VALUES(1,1,$1)`, f.clock.Now().Add(time.Hour))
	f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params) VALUES(9007199254740993,'WIREGUARD',51820,'{}')`)
	f.exec(t, `UPDATE nodes SET status='ONLINE'`)
	list := &node.ListService{Pool: f.pool, Clock: f.clock}
	views, staleTag, err := list.List(f.ctx, 1)
	if err != nil || len(views) != 1 || views[0].Available || *views[0].LockedReason != "OFFLINE" {
		t.Fatal("stale heartbeat still available", views, err)
	}
	result, err = runner.Step(f.ctx)
	if err != nil || result.State != "accepted" || result.Sequence != "2" {
		t.Fatal(result, err)
	}
	views, freshTag, err := list.List(f.ctx, 1)
	if err != nil || !views[0].Available || views[0].ID != "9007199254740993" || views[0].Protocols[0] != "WIREGUARD" || freshTag == staleTag {
		t.Fatal("fresh candidate unavailable", views, err)
	}
	f.clock.Add(91 * time.Second)
	views, tag, err := list.List(f.ctx, 1)
	if err != nil || views[0].Available || tag == freshTag {
		t.Fatal("time-only expiry kept old version", err)
	}
	result, err = runner.Step(f.ctx)
	if err != nil || result.State != "accepted" {
		t.Fatal(result, err)
	}
	f.exec(t, `UPDATE nodes SET agent_cert_fingerprint=$1`, strings.Repeat("c", 64))
	views, _, err = list.List(f.ctx, 1)
	if err != nil || views[0].Available || *views[0].LockedReason != "NOT_READY" {
		t.Fatal("old certificate receipt authorized new pin", views, err)
	}
	mode.Store(2)
	f.clock.Add(30 * time.Second)
	if _, err := runner.Step(f.ctx); err == nil {
		t.Fatal("redirect accepted")
	}
	mode.Store(0)
	if _, err := runner.Step(f.ctx); err == nil {
		t.Fatal("revoked certificate accepted")
	}
	f.exec(t, `UPDATE users SET status='SUSPENDED'`)
	if _, _, err := list.List(f.ctx, 1); err == nil {
		t.Fatal("suspended user read node data")
	}
}

func TestNodeIdentityMigrationDoesNotBackfillTrustIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	if _, err := f.accept(heartbeatAt(f.clock, "1")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../db/migrations/00019_node_heartbeat_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "-- +goose Down")
	f.exec(t, parts[1])
	f.exec(t, parts[0])
	var pin *string
	var seq int64
	if err := f.owner.QueryRow(f.ctx, `SELECT sequence,cert_fingerprint FROM node_heartbeats`).Scan(&seq, &pin); err != nil || seq != 1 || pin != nil {
		t.Fatal("migration invented identity or reset watermark", err)
	}
	if _, err := f.accept(heartbeatAt(f.clock, "1")); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.QueryRow(f.ctx, `SELECT cert_fingerprint FROM node_heartbeats`).Scan(&pin); err != nil || pin != nil {
		t.Fatal("duplicate upgraded trust", err)
	}
}
