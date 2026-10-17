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
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type managedNodeFixture struct {
	*approvalFixture
	service *NodeReadService
}

func newManagedNodeFixture(t *testing.T) *managedNodeFixture {
	t.Helper()
	a := newApprovalFixture(t)
	f := &managedNodeFixture{a, &NodeReadService{Auth: a.svc.Auth}}
	before := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00017_admin_node_read.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, strings.Split(string(raw), "-- +goose Down")[0])
	if authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("migration granted access")
	}
	if authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='nodes.read' AND risk_level=0`) != 1 {
		t.Fatal("missing read capability")
	}
	role := pgx.Identifier{a.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"node_groups", "nodes", "node_inbounds"} {
		name := pgx.Identifier{a.schema, table}.Sanitize()
		f.write(t, "CREATE TABLE "+name+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
		f.write(t, "REVOKE ALL ON "+name+" FROM "+role+"; GRANT SELECT ON "+name+" TO "+role)
	}
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'nodes.read')`, a.role)
	f.write(t, `INSERT INTO node_groups(id,name,min_tier) OVERRIDING SYSTEM VALUE VALUES(1,'标准线路',1)`)
	// 显式 id 不会推进 identity 序列；不补这一步，后续自增插入会直接撞主键。
	f.write(t, `SELECT setval(pg_get_serial_sequence('node_groups','id'),(SELECT max(id) FROM node_groups))`)
	return f
}
func (f *managedNodeFixture) write(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *managedNodeFixture) node(t *testing.T, id int64, name, status, country string, last *time.Time) {
	f.write(t, `INSERT INTO nodes(id,group_id,name,country_code,city,region,host,status,capacity_users,online_count,load_score,agent_version,agent_cert_fingerprint,last_heartbeat_at,updated_at)
 OVERRIDING SYSTEM VALUE VALUES($1,1,$2,$3,'香港','ASIA','private-host-sentinel',$4,500,23,45,'1.0.0',$5,$6,$7)`, id, name, country, status, strings.Repeat("e", 64), last, f.clock.Now())
}
func TestManagedNodeProjectionPaginationAndDetailIntegration(t *testing.T) {
	f := newManagedNodeFixture(t)
	now := f.clock.Now()
	p := f.people[0]
	for i := int64(10); i < 35; i++ {
		country, status := "HK", "ONLINE"
		if i%2 == 0 {
			country, status = "JP", "MAINTENANCE"
		}
		f.node(t, i, fmt.Sprintf("线路%d", i), status, country, &now)
	}
	f.node(t, 9007199254740993, "Literal_%", "ONLINE", "HK", nil)
	f.write(t, `INSERT INTO node_inbounds(id,node_id,protocol,port,priority,status,public_params) OVERRIDING SYSTEM VALUE VALUES(9007199254740994,10,'WIREGUARD',51820,10,'ENABLED','{"private_key":"secret-params-sentinel"}')`)
	first, err := f.service.List(f.ctx, p, ManagedNodeFilter{})
	if err != nil || first.Total != "26" || len(first.Items) != 20 || first.Items[0].ID != "10" || first.Items[19].ID != "29" || !first.ObservedAt.Equal(now) {
		t.Fatal("first page", first, err)
	}
	second, err := f.service.List(f.ctx, p, ManagedNodeFilter{Page: 2})
	if err != nil || second.Total != "26" || len(second.Items) != 6 || second.Items[5].ID != "9007199254740993" || second.Items[5].HeartbeatState != "NEVER" {
		t.Fatal("second page precision", second, err)
	}
	filtered, err := f.service.List(f.ctx, p, ManagedNodeFilter{Status: "ONLINE", CountryCode: "HK", Search: "线路"})
	if err != nil || filtered.Total != "12" {
		t.Fatal("combined filters", filtered, err)
	}
	literal, err := f.service.List(f.ctx, p, ManagedNodeFilter{Search: "_%"})
	if err != nil || literal.Total != "1" || literal.Items[0].ID != "9007199254740993" {
		t.Fatal("wildcard injection", literal, err)
	}
	empty, err := f.service.List(f.ctx, p, ManagedNodeFilter{CountryCode: "DE"})
	if err != nil || empty.Total != "0" || empty.Items == nil {
		t.Fatal("empty result", empty, err)
	}
	beyond, err := f.service.List(f.ctx, p, ManagedNodeFilter{Page: 10000, PageSize: 50})
	if err != nil || beyond.Total != "26" || len(beyond.Items) != 0 {
		t.Fatal("beyond last", beyond, err)
	}
	detail, err := f.service.Get(f.ctx, p, 10)
	if err != nil || detail.Node.ID != "10" || len(detail.Inbounds) != 1 || detail.Inbounds[0].ID != "9007199254740994" || detail.SyncStatus != "UNAVAILABLE" {
		t.Fatal("detail", detail, err)
	}
	absent, err := f.service.Get(f.ctx, p, 11)
	if err != nil || absent.Inbounds == nil || len(absent.Inbounds) != 0 {
		t.Fatal("empty inbounds", absent, err)
	}
	_, err = f.service.Get(f.ctx, p, 9999)
	approvalCode(t, err, httpx.ErrNotFound)
	for _, out := range []any{first, second, detail} {
		raw, _ := json.Marshal(out)
		for _, secret := range []string{"private-", "secret-", "host", "agent_cert_fingerprint", "public_params", "private_key", "token"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("sensitive field escaped", secret)
			}
		}
	}
}

type nodeReadHook struct {
	prefix string
	after  func()
}
type nodeReadHookKey struct{}

func (h *nodeReadHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, nodeReadHookKey{}, strings.HasPrefix(d.SQL, h.prefix))
}
func (h *nodeReadHook) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	if hit, _ := ctx.Value(nodeReadHookKey{}).(bool); hit && d.Err == nil {
		h.after()
	}
}
func nodeHookService(t *testing.T, f *managedNodeFixture, prefix string, fn func()) *NodeReadService {
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.Tracer = &nodeReadHook{prefix, fn}
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	auth := *f.service.Auth
	auth.Pool = pool
	return &NodeReadService{Auth: &auth}
}
func TestManagedNodeReadSnapshotAndFreshAuthorityIntegration(t *testing.T) {
	for _, mode := range []string{"snapshot", "permission revoked", "risk changed", "session revoked", "admin disabled", "expired", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedNodeFixture(t)
			now := f.clock.Now()
			f.node(t, 1, "原线路", "ONLINE", "HK", &now)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			hits := 0
			svc := nodeHookService(t, f, "SELECT count(*) FROM nodes n", func() {
				hits++
				switch mode {
				case "snapshot":
					f.node(t, 2, "新线路", "ONLINE", "HK", nil)
					f.write(t, `UPDATE nodes SET status='OFFLINE' WHERE id=1`)
				case "permission revoked":
					f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='nodes.read'`, f.role)
				case "risk changed":
					f.write(t, `UPDATE admin_permissions SET risk_level=2 WHERE code='nodes.read'`)
				case "session revoked":
					f.write(t, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, f.people[0].SessionID, now)
				case "admin disabled":
					f.write(t, `UPDATE admin_users SET status='DISABLED' WHERE id=$1`, f.people[0].ID)
				case "expired":
					f.clock.Add(sessionTTL)
				case "cancelled":
					cancel()
				}
			})
			out, err := svc.List(ctx, f.people[0], ManagedNodeFilter{Status: "ONLINE"})
			if hits != 1 {
				t.Fatal("real query hook missing", hits)
			}
			if mode == "snapshot" {
				if err != nil || out.Total != "1" || len(out.Items) != 1 || out.Items[0].Name != "原线路" || out.Items[0].Status != "ONLINE" {
					t.Fatal("inconsistent snapshot", out, err)
				}
				return
			}
			if out.Items != nil || out.Total != "" {
				t.Fatal("partial data after denial")
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				approvalCode(t, err, httpx.ErrForbidden)
			}
		})
	}
}
func TestManagedNodeDetailAuthorityAndValidationIntegration(t *testing.T) {
	f := newManagedNodeFixture(t)
	f.node(t, 1, "线路", "ONLINE", "HK", nil)
	svc := nodeHookService(t, f, "SELECT id::text,protocol,port", func() {
		f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='nodes.read'`, f.role)
	})
	out, err := svc.Get(f.ctx, f.people[0], 1)
	approvalCode(t, err, httpx.ErrForbidden)
	if out.Node.ID != "" || out.Inbounds != nil {
		t.Fatal("detail leaked after revoke")
	}
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'nodes.read')`, f.role)
	f.write(t, `UPDATE nodes SET online_count=-1 WHERE id=1`)
	_, err = f.service.Get(f.ctx, f.people[0], 1)
	approvalCode(t, err, httpx.ErrInternal)
	f.write(t, `UPDATE nodes SET online_count=0 WHERE id=1`)
	for i := 1; i <= 101; i++ {
		f.write(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params) VALUES(1,'TROJAN',$1,'{}')`, 1000+i)
	}
	out, err = f.service.Get(f.ctx, f.people[0], 1)
	approvalCode(t, err, httpx.ErrInternal)
	if out.Inbounds != nil {
		t.Fatal("truncated inbounds returned success")
	}
}
func TestManagedNodeHTTPStrictQueriesAndReadOnlyIntegration(t *testing.T) {
	f := newManagedNodeFixture(t)
	f.node(t, 1, "线路", "ONLINE", "HK", nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: f.service.Auth, SecureCookies: true}
	(&NodeReadHandler{Auth: h, Service: f.service}).Register(r.Group("/admin/v1"))
	send := func(method, path string, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://admin.example/admin/v1"+path, nil)
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Origin", "https://admin.example")
		if cookie {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := send("GET", "/nodes", false); w.Code != 401 {
		t.Fatal("no session", w.Code)
	}
	for _, path := range []string{"/nodes?page=0", "/nodes?page=1&page=2", "/nodes?page_size=51", "/nodes?search=", "/nodes?search=%20", "/nodes?search=x%0a", "/nodes?status=online", "/nodes?country_code=hk", "/nodes?host=secret", "/nodes?status=ONLINE&status=ONLINE", "/nodes/0", "/nodes/01", "/nodes/9223372036854775808", "/nodes/1?token=unexpected"} {
		if w := send("GET", path, true); w.Code != 400 {
			t.Fatal("invalid accepted", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/nodes", "/nodes/1", "/nodes?status=ONLINE&country_code=HK"} {
		if w := send("GET", path, true); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("read failed", path, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if w := send(method, "/nodes/1", true); w.Code != 404 && w.Code != 405 {
			t.Fatal("unexpected write route", method, w.Code)
		}
	}
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='nodes.read'`, f.role)
	for _, path := range []string{"/nodes", "/nodes/1", "/nodes/999"} {
		if w := send("GET", path, true); w.Code != 403 || strings.Contains(w.Body.String(), "heartbeat_state") {
			t.Fatal("permission check disclosed node", w.Code)
		}
	}
	if n := authCount(t, f.service.Auth, `SELECT count(*) FROM nodes WHERE status='ONLINE'`); n != 1 {
		t.Fatal("read changed node")
	}
}
