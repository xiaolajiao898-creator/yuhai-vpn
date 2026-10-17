package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 这个视图最容易出的错是「把未确认说成已确认」。下面的用例分别盯住：
// 权限必须独立持有、未确认节点不得携带任何证据、部分覆盖不算确认、
// 没有凭据时不得凭空确认、以及跨用户隔离。

const receiptNodeA, receiptNodeB = int64(9007199254740993), int64(2)

func newReceiptViewFixture(t *testing.T) *managedUserFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"device_credentials", "node_groups", "nodes"} {
		name := pgx.Identifier{f.schema, table}.Sanitize()
		f.write(t, "CREATE TABLE "+name+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
		f.write(t, "REVOKE ALL ON "+name+" FROM "+role+"; GRANT SELECT ON "+name+" TO "+role)
	}
	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00021_node_credential_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err := f.owner.Exec(f.ctx, up); err != nil {
		t.Fatal("实际迁移执行失败", err)
	}
	if after := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`); after != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='devices.receipts' AND risk_level=0`); n != 1 {
		t.Fatal("权限定义缺失", n)
	}
	f.write(t, "GRANT SELECT ON "+pgx.Identifier{f.schema, "node_credential_receipts"}.Sanitize()+" TO "+role)

	f.write(t, `INSERT INTO node_groups(id,name) OVERRIDING SYSTEM VALUE VALUES(1,'fixture')`)
	for _, n := range []struct {
		id     int64
		name   string
		status string
	}{{receiptNodeA, "东京 01", "ONLINE"}, {receiptNodeB, "法兰克福 02", "MAINTENANCE"}} {
		f.write(t, `INSERT INTO nodes(id,group_id,name,country_code,city,region,host,status,capacity_users,updated_at)
 OVERRIDING SYSTEM VALUE VALUES($1,1,$2,'JP','Tokyo','ASIA','private-host.invalid',$3,100,$4)`, n.id, n.name, n.status, f.clock.Now())
	}
	return f
}

// credential 为设备挂一条凭据并返回其编号。
func credential(t *testing.T, f *managedUserFixture, id, deviceID int64) int64 {
	t.Helper()
	f.write(t, `INSERT INTO device_credentials(id,device_id,proxy_uuid,proxy_secret_enc,status)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,'REVOKED')`, id, deviceID, uuid.NewString(), []byte("private-secret-sentinel"))
	return id
}

func writeReceipt(t *testing.T, f *managedUserFixture, nodeID, credentialID, sequence int64, at time.Time) {
	t.Helper()
	f.write(t, `INSERT INTO node_credential_receipts(node_id,credential_id,confirmed_absent_at,heartbeat_sequence,snapshot_checksum)
 VALUES($1,$2,$3,$4,$5)`, nodeID, credentialID, at, sequence, strings.Repeat("a", 64))
}

func deviceWithCredentials(t *testing.T, f *managedUserFixture, deviceID int64, credentials ...int64) {
	t.Helper()
	addManagedDevice(t, f, deviceID, f.userID, "REVOKED", "ANDROID", nil, nil, nil)
	for _, c := range credentials {
		credential(t, f, c, deviceID)
	}
}

func TestReceiptsRequireIndependentCapabilityIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	deviceWithCredentials(t, f, 100, 1001)
	p := f.people[0]

	// 只有 users.read 时必须拒绝：回执是独立能力。
	_, err := f.service.Receipts(f.ctx, p, f.userID, 100)
	approvalCode(t, err, httpx.ErrForbidden)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'devices.receipts')`, f.role)
	if _, err := f.service.Receipts(f.ctx, p, f.userID, 100); err != nil {
		t.Fatalf("授予能力后仍被拒绝：%v", err)
	}
}

func grantReceipts(t *testing.T, f *managedUserFixture) {
	t.Helper()
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'devices.receipts')`, f.role)
}

func TestReceiptsConfirmPerNodeIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	// device_credentials.device_id 唯一：一台设备当前只有一条凭据。
	deviceWithCredentials(t, f, 100, 1001)
	at := f.clock.Now().Add(-time.Minute)
	// A 节点已确认；B 节点没有任何回执。
	writeReceipt(t, f, receiptNodeA, 1001, 9, at)

	out, err := f.service.Receipts(f.ctx, f.people[0], f.userID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != "100" || out.DeviceStatus != "REVOKED" || out.CredentialCount != "1" {
		t.Fatalf("摘要字段不符：%+v", out)
	}
	if out.ConfirmedNodeCount != "1" || len(out.Items) != 2 {
		t.Fatalf("确认节点数不符：%+v", out)
	}
	byNode := map[string]DeviceRevocationReceipt{}
	for _, r := range out.Items {
		byNode[r.NodeID] = r
	}
	a, b := byNode["9007199254740993"], byNode["2"]
	if !a.Confirmed || a.ConfirmedAbsentAt == nil || a.HeartbeatSequence == nil || *a.HeartbeatSequence != "9" || a.SnapshotChecksum == nil {
		t.Fatalf("已确认节点应带齐证据：%+v", a)
	}
	if a.NodeName != "东京 01" || a.NodeStatus != "ONLINE" || !a.ConfirmedAbsentAt.Equal(at) {
		t.Fatalf("节点展示字段不符：%+v", a)
	}
	// 未确认节点必须如实列出，且不得携带任何确认证据。
	if b.Confirmed || b.ConfirmedAbsentAt != nil || b.HeartbeatSequence != nil || b.SnapshotChecksum != nil {
		t.Fatalf("未确认节点被当作已确认或带出了证据：%+v", b)
	}
	if b.NodeStatus != "MAINTENANCE" {
		t.Fatalf("非在线节点也必须如实列出：%+v", b)
	}
}

// 回执按凭据记录：同一台设备换发凭据后，旧凭据的回执不得算到新凭据头上。
func TestReceiptsDoNotCountOtherCredentialsIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	deviceWithCredentials(t, f, 110, 1101)
	deviceWithCredentials(t, f, 111, 1102)
	// 只有另一台设备的凭据被确认。
	writeReceipt(t, f, receiptNodeA, 1102, 5, f.clock.Now())

	out, err := f.service.Receipts(f.ctx, f.people[0], f.userID, 110)
	if err != nil {
		t.Fatal(err)
	}
	if out.ConfirmedNodeCount != "0" {
		t.Fatalf("别的凭据的回执被算到了本设备头上：%+v", out)
	}
	other, err := f.service.Receipts(f.ctx, f.people[0], f.userID, 111)
	if err != nil || other.ConfirmedNodeCount != "1" {
		t.Fatalf("本设备自己的回执未被计入：%+v %v", other, err)
	}
}

func TestReceiptsNeverConfirmWithoutCredentialsIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	// 设备没有任何凭据：任何节点都无从确认，不得因为「没有待移除项」就报已确认。
	addManagedDevice(t, f, 101, f.userID, "REVOKED", "ANDROID", nil, nil, nil)

	out, err := f.service.Receipts(f.ctx, f.people[0], f.userID, 101)
	if err != nil {
		t.Fatal(err)
	}
	if out.CredentialCount != "0" || out.ConfirmedNodeCount != "0" || len(out.Items) != 2 {
		t.Fatalf("无凭据设备的结果不符：%+v", out)
	}
	for _, r := range out.Items {
		if r.Confirmed {
			t.Fatalf("无凭据却报告已确认：%+v", r)
		}
	}
}

func TestReceiptsListEveryNodeIncludingUnconfirmedIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	deviceWithCredentials(t, f, 102, 1003)
	// 一条回执都没有：必须仍然列出全部节点，否则看起来像「没有节点需要确认」。
	out, err := f.service.Receipts(f.ctx, f.people[0], f.userID, 102)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.ConfirmedNodeCount != "0" {
		t.Fatalf("未确认时应列出全部节点：%+v", out)
	}
	for _, r := range out.Items {
		if r.Confirmed || r.SnapshotChecksum != nil {
			t.Fatalf("未确认节点携带了确认信息：%+v", r)
		}
	}
}

func TestReceiptsEnforceOwnershipAndInputIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	deviceWithCredentials(t, f, 103, 1004)
	p := f.people[0]

	// 设备属于别的用户时按不存在处理，不得泄露其存在。
	_, err := f.service.Receipts(f.ctx, p, f.emptyID, 103)
	approvalCode(t, err, httpx.ErrNotFound)
	_, err = f.service.Receipts(f.ctx, p, f.userID, 777777)
	approvalCode(t, err, httpx.ErrNotFound)
	for _, bad := range [][2]int64{{0, 103}, {f.userID, 0}, {-1, 103}, {f.userID, -1}} {
		_, err = f.service.Receipts(f.ctx, p, bad[0], bad[1])
		approvalCode(t, err, httpx.ErrBadRequest)
	}
}

// 路由与 HTTP 层：cookie 守卫、拒绝任何查询参数、no-store，以及响应体形状。
func TestReceiptsHTTPRouteIntegration(t *testing.T) {
	f := newReceiptViewFixture(t)
	grantReceipts(t, f)
	deviceWithCredentials(t, f, 120, 1201)
	writeReceipt(t, f, receiptNodeA, 1201, 4, f.clock.Now())

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: f.svc.Auth, SecureCookies: true}
	(&UserDevicesHandler{Auth: h, Service: f.service}).Register(r.Group("/admin/v1"))
	send := func(path string, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "https://admin.example/admin/v1"+path, nil)
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Origin", "https://admin.example")
		if cookie {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	path := fmt.Sprintf("/users/%d/devices/120/receipts", f.userID)
	if w := send(path, false); w.Code != http.StatusUnauthorized {
		t.Fatal("缺少 cookie 未被拦截", w.Code)
	}
	// 本接口没有任何查询参数，多余参数一律 400。
	for _, query := range []string{"?page=1", "?node_id=1", "?token=unexpected", "?confirmed=true"} {
		if w := send(path+query, true); w.Code != http.StatusBadRequest {
			t.Fatal("接受了多余查询参数", query, w.Code)
		}
	}
	for _, bad := range []string{"/users/0/devices/120/receipts", "/users/%d/devices/0/receipts", "/users/%d/devices/01/receipts"} {
		p := bad
		if strings.Contains(bad, "%d") {
			p = fmt.Sprintf(bad, f.userID)
		}
		if w := send(p, true); w.Code != http.StatusBadRequest {
			t.Fatal("接受了非法编号", p, w.Code)
		}
	}
	w := send(path, true)
	if w.Code != http.StatusOK {
		t.Fatal("正常请求失败", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("缺少 no-store", w.Header().Get("Cache-Control"))
	}
	var out DeviceRevocationReceipts
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.DeviceID != "120" || out.CredentialCount != "1" || out.ConfirmedNodeCount != "1" || len(out.Items) != 2 {
		t.Fatalf("响应体不符：%+v", out)
	}
	// 私有字段绝不出现在响应里。
	for _, secret := range []string{"private-host.invalid", "proxy_secret", "private-secret-sentinel"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("响应泄露了 %q", secret)
		}
	}
}
