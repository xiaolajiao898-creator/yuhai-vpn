package nodecontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wgTestKey(n byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{n}, 32)) }
func newWGFixture(t *testing.T) *heartbeatFixture {
	f := newHeartbeatFixture(t)
	for _, table := range []string{"node_inbounds", "users", "devices", "device_credentials", "subscriptions", "traffic_packs", "device_addons"} {
		f.exec(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, sql := range []string{
		"GRANT SELECT ON node_groups,node_inbounds TO " + role,
		"GRANT SELECT(id,status) ON users TO " + role,
		"GRANT SELECT(id,user_id,status) ON devices TO " + role,
		"GRANT SELECT(id,device_id,status,wg_pubkey,wg_address) ON device_credentials TO " + role,
		"GRANT SELECT(user_id,tier,device_limit,expire_at,period_quota_bytes,period_used_bytes) ON subscriptions TO " + role,
		"GRANT SELECT(user_id,tier,device_limit,expire_at,status,used_bytes,total_bytes) ON traffic_packs TO " + role,
		"GRANT SELECT(user_id,quantity,expire_at) ON device_addons TO " + role,
	} {
		f.exec(t, sql)
	}
	f.exec(t, `UPDATE nodes SET status='ONLINE'`)
	config := fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"1"}`, wgTestKey(1))
	f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params) VALUES(9007199254740993,'WIREGUARD',51820,$1)`, config)
	f.svc.WireGuardManifestEnabled = true
	return f
}
func addWGUser(t *testing.T, f *heartbeatFixture, id int) {
	lookup := sha256.Sum256([]byte(fmt.Sprint(id)))
	f.exec(t, `INSERT INTO users(id,account_no,secret_lookup,invite_code) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4)`, int64(id), fmt.Sprintf("%010d", id), hex.EncodeToString(lookup[:]), fmt.Sprint(id))
	f.exec(t, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform) OVERRIDING SYSTEM VALUE VALUES($1,$1,$2,'non-wg-device-signing-public-key','ANDROID')`, int64(id), uuid.NewString())
	f.exec(t, `INSERT INTO device_credentials(id,device_id,proxy_uuid,proxy_secret_enc,wg_pubkey,wg_address) OVERRIDING SYSTEM VALUE VALUES($1,$1,$2,$3,$4,$5::inet)`, int64(id), uuid.NewString(), []byte("secret-not-selected"), wgTestKey(byte(id)), fmt.Sprintf("10.20.0.%d/32", id))
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,device_limit,expire_at,period_quota_bytes) VALUES($1,1,1,$2,1000)`, int64(id), f.clock.Now().Add(time.Hour))
}
func getWG(t *testing.T, f *heartbeatFixture) nodewire.WGLease {
	t.Helper()
	l, e := f.svc.WireGuardManifest(f.ctx, f.ca.state(f.cert), strings.Repeat("a", 32))
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func TestWGManifestEligibilityAndNoSecretReadIntegration(t *testing.T) {
	f := newWGFixture(t)
	for i := 10; i < 20; i++ {
		addWGUser(t, f, i)
	}
	f.exec(t, `UPDATE users SET status='BANNED' WHERE id=11`)
	f.exec(t, `UPDATE devices SET status='REVOKED' WHERE id=12`)
	f.exec(t, `UPDATE device_credentials SET status='REVOKED' WHERE id=13`)
	f.exec(t, `UPDATE subscriptions SET expire_at=$1 WHERE user_id=14`, f.clock.Now())
	f.exec(t, `UPDATE subscriptions SET period_used_bytes=1000 WHERE user_id=15`)
	f.exec(t, `UPDATE subscriptions SET tier=0 WHERE user_id=16`)
	f.exec(t, `UPDATE device_credentials SET wg_pubkey=NULL,wg_address=NULL WHERE id=17`)
	f.exec(t, `UPDATE subscriptions SET device_limit=0 WHERE user_id=18`)
	f.exec(t, `UPDATE subscriptions SET expire_at=$1 WHERE user_id=19`, f.clock.Now().Add(7*time.Second))
	l := getWG(t, f)
	if len(l.Manifest.Peers) != 2 || l.Manifest.Peers[0].CredentialID != "10" || l.Manifest.Peers[1].CredentialID != "19" {
		t.Fatal("ineligible credentials disclosed", l.Manifest.Peers)
	}
	if l.ExpiresAt != f.clock.Now().Add(7*time.Second).Format(time.RFC3339Nano) {
		t.Fatal("lease outlived entitlement")
	}
	raw, _ := json.Marshal(l)
	if bytes.Contains(raw, []byte("secret")) || bytes.Contains(raw, []byte("proxy_uuid")) {
		t.Fatal("non-WG material disclosed")
	}
	if _, e := f.pool.Exec(f.ctx, `SELECT proxy_secret_enc FROM device_credentials`); e == nil {
		t.Fatal("test role can read encrypted proxy secret")
	}
	f.exec(t, `UPDATE device_credentials SET status='REVOKED' WHERE id=10`)
	if len(getWG(t, f).Manifest.Peers) != 1 {
		t.Fatal("revoke not reflected")
	}
	f.clock.Add(7 * time.Second)
	if len(getWG(t, f).Manifest.Peers) != 0 {
		t.Fatal("expiry not reflected")
	}
}
func TestWGManifestTrafficRightsAndDeviceLimitIntegration(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 10)
	f.exec(t, `UPDATE node_groups SET min_tier=2`)
	if len(getWG(t, f).Manifest.Peers) != 0 {
		t.Fatal("tier ignored")
	}
	f.exec(t, `INSERT INTO traffic_packs(user_id,tier,device_limit,total_bytes,expire_at,source_type,source_id) VALUES(10,2,1,1000,$1,'ADMIN',1)`, f.clock.Now().Add(time.Hour))
	if len(getWG(t, f).Manifest.Peers) != 1 {
		t.Fatal("valid pack excluded")
	}
	f.exec(t, `INSERT INTO devices(user_id,device_uid,pubkey,platform) VALUES(10,$1,'test','IOS')`, uuid.NewString())
	if len(getWG(t, f).Manifest.Peers) != 0 {
		t.Fatal("over device limit allowed")
	}
	f.exec(t, `INSERT INTO device_addons(user_id,quantity,expire_at,source_type,source_id) VALUES(10,1,$1,'ADMIN',1)`, f.clock.Now().Add(5*time.Second))
	if len(getWG(t, f).Manifest.Peers) != 1 {
		t.Fatal("addon excluded")
	}
	f.clock.Add(5 * time.Second)
	if len(getWG(t, f).Manifest.Peers) != 0 {
		t.Fatal("expired addon allowed")
	}
	f.exec(t, `UPDATE devices SET status='REVOKED' WHERE user_id=10 AND id<>10`)
	f.exec(t, `UPDATE traffic_packs SET used_bytes=1000`)
	if len(getWG(t, f).Manifest.Peers) != 0 {
		t.Fatal("exhausted pack allowed")
	}
}
func TestWGManifestFailsOnPartialInvalidAndDuplicateConfigIntegration(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 10)
	for _, sql := range []string{`UPDATE device_credentials SET wg_address=NULL`, `UPDATE device_credentials SET wg_address='8.8.8.8/32'`, `UPDATE device_credentials SET wg_address='10.20.0.0/24'`} {
		f.exec(t, sql)
		_, e := f.svc.WireGuardManifest(f.ctx, f.ca.state(f.cert), strings.Repeat("a", 32))
		expectCode(t, e, httpx.ErrNodeUnavailable)
	}
	f.exec(t, `UPDATE device_credentials SET wg_address='10.20.0.10/32'`)
	f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params) SELECT node_id,protocol,51821,public_params FROM node_inbounds LIMIT 1`)
	_, e := f.svc.WireGuardManifest(f.ctx, f.ca.state(f.cert), strings.Repeat("a", 32))
	expectCode(t, e, httpx.ErrNodeUnavailable)
	f.exec(t, `UPDATE nodes SET status='DISABLED'`)
	_, e = f.svc.WireGuardManifest(f.ctx, f.ca.state(f.cert), strings.Repeat("a", 32))
	expectCode(t, e, httpx.ErrForbidden)
}
func TestWGManifestHTTPGateAndStrictRequestIntegration(t *testing.T) {
	f := newWGFixture(t)
	for _, tc := range []struct {
		nonce, path, body string
		want              int
	}{{strings.Repeat("b", 32), "/node/v1/wireguard/manifest", "", 200}, {"", "/node/v1/wireguard/manifest", "", 400}, {strings.Repeat("b", 32), "/node/v1/wireguard/manifest?node_id=2", "", 400}, {strings.Repeat("b", 32), "/node/v1/wireguard/manifest", "{}", 400}} {
		req := httptest.NewRequest("GET", tc.path, strings.NewReader(tc.body))
		req.TLS = f.ca.state(f.cert)
		req.Header.Set("X-Snapshot-Nonce", tc.nonce)
		w := httptest.NewRecorder()
		f.svc.Handler().ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache header missing")
		}
	}
	f.svc.WireGuardManifestEnabled = false
	w := httptest.NewRecorder()
	f.svc.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/node/v1/wireguard/manifest", nil))
	if w.Code != 404 {
		t.Fatal("disabled route exposed")
	}
}
