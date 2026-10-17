package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func addManagedDevice(t *testing.T, f *managedUserFixture, id, userID int64, status, platform string, name *string, seenAt, revokedAt *time.Time) {
	t.Helper()
	f.write(t, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform,model,os_version,app_version,fingerprint_hash,display_name,status,created_at,last_seen_at,revoked_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,'private-public-key-sentinel',$4,'private-model-sentinel','private-os-sentinel','private-app-sentinel',$5,$6,$7,$8,$9,$10)`, id, userID, uuid.NewString(), platform, strings.Repeat("e", 64), name, status, f.clock.Now(), seenAt, revokedAt)
}

func TestManagedDeviceProjectionPaginationAndIsolationIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	p := f.people[0]
	for i := 10; i < 34; i++ {
		status := "ACTIVE"
		if i%2 != 0 {
			status = "REVOKED"
		}
		addManagedDevice(t, f, int64(i), f.emptyID, status, "ANDROID", nil, nil, nil)
	}
	name := "<b>用户的 iPhone</b>"
	revoked, seen := f.clock.Now().Add(time.Hour), f.clock.Now().Add(2*time.Hour)
	addManagedDevice(t, f, 9007199254740993, f.emptyID, "REVOKED", "IOS", &name, &seen, &revoked)
	first, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{})
	if err != nil || first.UserID != strconv.FormatInt(f.emptyID, 10) || first.Total != "25" || len(first.Items) != 20 || first.Page != 1 || first.PageSize != 20 {
		t.Fatal("first page", first, err)
	}
	if first.Items[0].ID != "9007199254740993" || first.Items[1].ID != "33" || first.Items[19].ID != "15" || first.Items[0].DisplayName == nil || *first.Items[0].DisplayName != name || first.Items[0].RevokedAt == nil || !first.Items[0].LastSeenAt.Equal(seen) {
		t.Fatal("numeric sorting/metadata", first)
	}
	second, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{Page: 2})
	if err != nil || second.Total != "25" || len(second.Items) != 5 || second.Items[0].ID != "14" || second.Items[4].ID != "10" || second.Items[0].LastSeenAt != nil || second.Items[0].DisplayName != nil {
		t.Fatal("second page or nulls", second, err)
	}
	for _, status := range []string{"ACTIVE", "REVOKED"} {
		page, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{Status: status, PageSize: 50})
		want := "12"
		if status == "REVOKED" {
			want = "13"
		}
		if err != nil || page.Total != want || strconv.Itoa(len(page.Items)) != want {
			t.Fatal("status count", status, page, err)
		}
		for _, d := range page.Items {
			if d.Status != status {
				t.Fatal("filter escaped", d)
			}
		}
	}
	other, err := f.service.Devices(f.ctx, p, f.userID, ManagedDeviceFilter{})
	if err != nil || other.Total != "3" || len(other.Items) != 3 {
		t.Fatal("cross-user rows or count", other, err)
	}
	f.user(t, 7, "7777777777", "ACTIVE")
	empty, err := f.service.Devices(f.ctx, p, 7, ManagedDeviceFilter{})
	if err != nil || empty.Total != "0" || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal("existing user empty list", empty, err)
	}
	beyond, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{Page: 10000, PageSize: 50})
	if err != nil || beyond.Total != "25" || len(beyond.Items) != 0 {
		t.Fatal("maximum valid page", beyond, err)
	}
	_, err = f.service.Devices(f.ctx, p, 777777, ManagedDeviceFilter{})
	approvalCode(t, err, httpx.ErrNotFound)
	for _, out := range []ManagedDevicePage{first, second, other, empty} {
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private-", "device_uid", "pubkey", "fingerprint", "model", "os_version", "app_version", "secret", "token", "risk", "ip_hash"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("sensitive field leaked", secret)
			}
		}
	}
	// No ledger permission is required for these public device fields.
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.ledgers.read'`, f.role)
	if _, err := f.service.Devices(f.ctx, p, f.emptyID, ManagedDeviceFilter{}); err != nil {
		t.Fatal("unrelated ledger permission required", err)
	}
}

// Pause the actual Devices call after the count completes. A second database
// connection can commit changes while the first read-only snapshot is open.
// This proves the entry point uses the same snapshot and the final auth check.
type deviceCountHook struct{ afterCount func() }
type deviceCountContextKey struct{}

func (h *deviceCountHook) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, deviceCountContextKey{}, strings.HasPrefix(data.SQL, "SELECT count(*) FROM devices d"))
}
func (h *deviceCountHook) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if hit, _ := ctx.Value(deviceCountContextKey{}).(bool); hit && data.Err == nil {
		h.afterCount()
	}
}
func deviceServiceAfterCount(t *testing.T, f *managedUserFixture, hook func()) *UserReadService {
	t.Helper()
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.Tracer = &deviceCountHook{afterCount: hook}
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	auth := *f.svc.Auth
	auth.Pool = pool
	return &UserReadService{Auth: &auth}
}

func TestManagedDeviceReadSnapshotAndAuthorityIntegration(t *testing.T) {
	for _, mode := range []string{"stable snapshot", "permission revoked", "session revoked", "admin disabled", "session expired", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedUserFixture(t)
			p := f.people[0]
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			hits := 0
			svc := deviceServiceAfterCount(t, f, func() {
				hits++
				switch mode {
				case "stable snapshot":
					f.write(t, `UPDATE devices SET status='REVOKED',revoked_at=$2 WHERE user_id=$1 AND status='ACTIVE'`, f.userID, f.clock.Now())
					addManagedDevice(t, f, 99, f.userID, "ACTIVE", "IOS", nil, nil, nil)
				case "permission revoked":
					f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
				case "session revoked":
					f.write(t, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, p.SessionID, f.clock.Now())
				case "admin disabled":
					f.write(t, `UPDATE admin_users SET status='DISABLED' WHERE id=$1`, p.ID)
				case "session expired":
					f.clock.Add(sessionTTL)
				case "cancelled":
					cancel()
				}
			})
			out, err := svc.Devices(ctx, p, f.userID, ManagedDeviceFilter{Status: "ACTIVE"})
			if hits != 1 {
				t.Fatal("test hook did not reach actual device count", hits)
			}
			if mode == "stable snapshot" {
				if err != nil || out.Total != "2" || len(out.Items) != 2 {
					t.Fatal("count and rows differ across commits", out, err)
				}
				for _, d := range out.Items {
					if d.ID == "99" || d.Status != "ACTIVE" || d.RevokedAt != nil {
						t.Fatal("new commit escaped original snapshot", d)
					}
				}
				current, err := f.service.Devices(f.ctx, p, f.userID, ManagedDeviceFilter{Status: "ACTIVE"})
				if err != nil || current.Total != "1" || len(current.Items) != 1 || current.Items[0].ID != "99" {
					t.Fatal("second read did not observe new state", current, err)
				}
				return
			}
			if out.UserID != "" || out.Items != nil || out.Total != "" {
				t.Fatal("failed authorization/cancellation retained data", out)
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled query succeeded", err)
				}
				if _, err := f.service.Devices(f.ctx, p, f.userID, ManagedDeviceFilter{}); err != nil {
					t.Fatal("cancelled read leaked resources", err)
				}
			} else {
				approvalCode(t, err, httpx.ErrForbidden)
			}
		})
	}
}

func TestManagedDeviceInvalidStoredDataFailsClosedIntegration(t *testing.T) {
	for _, mode := range []string{"active with revocation time", "control in display name"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedUserFixture(t)
			if mode == "active with revocation time" {
				f.write(t, `UPDATE devices SET revoked_at=$2 WHERE user_id=$1 AND status='ACTIVE'`, f.userID, f.clock.Now())
			} else {
				f.write(t, `UPDATE devices SET display_name=$2 WHERE user_id=$1`, f.userID, "private\nname")
			}
			out, err := f.service.Devices(f.ctx, f.people[0], f.userID, ManagedDeviceFilter{})
			approvalCode(t, err, httpx.ErrInternal)
			if out.Items != nil || out.UserID != "" {
				t.Fatal("invalid result retained partial data")
			}
		})
	}
}

func TestManagedDeviceHTTPStrictQueriesAndNoWritesIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: f.svc.Auth, SecureCookies: true}
	(&UserDevicesHandler{Auth: h, Service: f.service}).Register(r.Group("/admin/v1"))
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
	path := fmt.Sprintf("/users/%d/devices", f.userID)
	if w := send("GET", path, false); w.Code != http.StatusUnauthorized {
		t.Fatal("cookie guard", w.Code)
	}
	for _, query := range []string{"?page=0", "?page=10001", "?page_size=0", "?page_size=51", "?page=-1", "?page=01", "?page=1&page=2", "?status=", "?status=active", "?status=SUSPENDED", "?status=ACTIVE&status=REVOKED", "?user_id=5", "?token=unexpected", "?page_size=", "?page_size=20&page_size=20"} {
		if w := send("GET", path+query, true); w.Code != http.StatusBadRequest {
			t.Fatal("invalid query accepted", query, w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"0", "-1", "05", "1e3", "9223372036854775808", "not-an-id"} {
		if w := send("GET", "/users/"+id+"/devices", true); w.Code != http.StatusBadRequest {
			t.Fatal("invalid target accepted", id, w.Code)
		}
	}
	for _, suffix := range []string{"", "?status=ACTIVE", "?status=REVOKED", "?page=10000&page_size=50"} {
		w := send("GET", path+suffix, true)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("valid GET failed", suffix, w.Code, w.Body.String())
		}
	}
	if w := send("GET", "/users/777777/devices", true); w.Code != http.StatusNotFound {
		t.Fatal("missing user", w.Code)
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, candidate := range []string{path, path + "/1", "/devices/1"} {
			if w := send(method, candidate, true); w.Code != 404 && w.Code != 405 {
				t.Fatal("write route exposed", method, candidate, w.Code)
			}
		}
	}
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
	for _, candidate := range []string{path, "/users/777777/devices"} {
		if w := send("GET", candidate, true); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "platform") {
			t.Fatal("permission or target existence leaked", w.Code, w.Body.String())
		}
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM devices WHERE user_id=$1 AND status='ACTIVE'`, f.userID); n != 2 {
		t.Fatal("read handler changed bindings")
	}
}
