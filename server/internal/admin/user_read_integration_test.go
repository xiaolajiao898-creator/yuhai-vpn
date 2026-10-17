package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type managedUserFixture struct {
	*approvalFixture
	service *UserReadService
	userID  int64
	emptyID int64
}

func newManagedUserFixture(t *testing.T) *managedUserFixture {
	t.Helper()
	a := newApprovalFixture(t)
	f := &managedUserFixture{approvalFixture: a, service: &UserReadService{Auth: a.svc.Auth}, userID: 9007199254740993, emptyID: 5}
	before := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00015_admin_user_read.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err = a.owner.Exec(a.ctx, up); err != nil {
		t.Fatal("actual migration", err)
	}
	if after := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`); after != before {
		t.Fatal("migration assigned privileges")
	}
	if n := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code IN('users.read','users.ledgers.read') AND risk_level=0`); n != 2 {
		t.Fatal("permission definitions", n)
	}
	role := pgx.Identifier{a.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"users", "user_balances", "devices", "subscriptions", "balance_ledger", "subscription_ledger"} {
		name := pgx.Identifier{a.schema, table}.Sanitize()
		if _, err = a.owner.Exec(a.ctx, "CREATE TABLE "+name+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
		// The exercised service can only read business tables in this schema.
		if _, err = a.owner.Exec(a.ctx, "REVOKE ALL ON "+name+" FROM "+role+"; GRANT SELECT ON "+name+" TO "+role); err != nil {
			t.Fatal(err)
		}
	}
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'users.read'),($1,'users.ledgers.read')`, a.role)
	for i := range a.people {
		a.people[i].Permissions = append(a.people[i].Permissions, userReadPermission, userLedgerPermission)
	}
	f.user(t, f.userID, "1234567890", "ACTIVE")
	f.user(t, f.emptyID, "5555555555", "SUSPENDED")
	f.write(t, `INSERT INTO user_balances(user_id,available_micro,updated_at) VALUES($1,$2,$3)`, f.userID, int64(math.MaxInt64), f.clock.Now())
	f.write(t, `INSERT INTO subscriptions(user_id,tier,expire_at,device_limit,period_quota_bytes,period_used_bytes,period_reset_at) VALUES($1,2,$2,3,$3,$4,$5)`, f.userID, f.clock.Now().Add(-time.Hour), int64(math.MaxInt64), int64(9007199254740993), f.clock.Now().Add(time.Hour))
	for i, status := range []string{"ACTIVE", "ACTIVE", "REVOKED"} {
		f.write(t, `INSERT INTO devices(user_id,device_uid,pubkey,platform,fingerprint_hash,status,created_at) VALUES($1,$2,'public-key-sentinel','ANDROID',$3,$4,$5)`, f.userID, uuid.NewString(), fmt.Sprintf("%064x", i+1), status, f.clock.Now())
	}
	f.write(t, `INSERT INTO balance_ledger(id,user_id,entry_type,amount_micro,balance_after_micro,source_type,source_id,idempotency_key,operator_type,operator_id,remark,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,'ADMIN_DEBIT',$3,$4,'ADMIN_APPROVAL',$5,'private-idempotency-sentinel','ADMIN',777,'private-remark-sentinel',$6)`, int64(9007199254740997), f.userID, int64(math.MinInt64), int64(math.MaxInt64), int64(9007199254740998), f.clock.Now())
	f.write(t, `INSERT INTO subscription_ledger(user_id,change_type,source_type,delta_seconds,delta_bytes,delta_devices,before_snapshot,after_snapshot,idempotency_key,operator_type,remark,created_at) VALUES($1,'ADMIN_ADJUST','SYSTEM',$2,$3,-3,'{"secret":"private-before-sentinel"}','{"token":"private-after-sentinel"}','private-entitlement-key','SYSTEM','private-entitlement-remark',$4)`, f.userID, int64(math.MinInt64), int64(math.MaxInt64), f.clock.Now())
	return f
}
func (f *managedUserFixture) write(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *managedUserFixture) user(t *testing.T, id int64, account, status string) {
	f.write(t, `INSERT INTO users(id,account_no,secret_lookup,invite_code,status,risk_flags,register_channel,register_ip_hash,register_ip_prefix_hash,created_at,updated_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,'{"token":"private-risk-sentinel"}','private-channel-sentinel',$6,$7,$8,$8)`, id, account, fmt.Sprintf("%064x", id), fmt.Sprintf("INV%d", id%10000), status, strings.Repeat("a", 64), strings.Repeat("b", 64), f.clock.Now())
}
func TestManagedUserReadPrecisionProjectionAndMissingRecordsIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	p := f.people[0]
	out, err := f.service.List(f.ctx, p, ManagedUserFilter{})
	if err != nil || out.Total != "2" || len(out.Items) != 2 || out.Items[0].ID != strconv.FormatInt(f.userID, 10) {
		t.Fatal("user list", out, err)
	}
	if out.Items[0].Balance == nil || *out.Items[0].Balance != "9223372036854.775807" || out.Items[0].ActiveDeviceCount != "2" {
		t.Fatal("balance/count precision", out.Items[0])
	}
	if out.Items[1].Balance != nil {
		t.Fatal("missing balance became zero")
	}
	detail, err := f.service.Get(f.ctx, p, f.userID)
	if err != nil || detail.Subscription == nil || detail.Subscription.PeriodUsedBytes != "9007199254740993" || detail.Subscription.PeriodQuotaBytes == nil || *detail.Subscription.PeriodQuotaBytes != "9223372036854775807" {
		t.Fatal("subscription projection", detail, err)
	}
	empty, err := f.service.Get(f.ctx, p, f.emptyID)
	if err != nil || empty.Balance != nil || empty.BalanceUpdatedAt != nil || empty.Subscription != nil {
		t.Fatal("missing records", empty, err)
	}
	balance, err := f.service.Ledgers(f.ctx, p, f.userID, "balance", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	items := balance.Items.([]ManagedBalanceEntry)
	if balance.Total != "1" || balance.Currency == nil || *balance.Currency != "USDT" || len(items) != 1 || items[0].ID != "9007199254740997" || items[0].Amount != "-9223372036854.775808" || items[0].BalanceAfter != "9223372036854.775807" || items[0].SourceID != "9007199254740998" {
		t.Fatal("ledger precision", balance)
	}
	rights, err := f.service.Ledgers(f.ctx, p, f.userID, "entitlement", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	changes := rights.Items.([]ManagedEntitlementEntry)
	if rights.Currency != nil || changes[0].SourceID != nil || changes[0].DeltaSeconds != "-9223372036854775808" || changes[0].DeltaBytes != "9223372036854775807" || changes[0].DeltaDevices != "-3" {
		t.Fatal("rights precision", rights)
	}
	none, err := f.service.Ledgers(f.ctx, p, f.emptyID, "balance", 1, 20)
	if err != nil || none.Total != "0" || len(none.Items.([]ManagedBalanceEntry)) != 0 {
		t.Fatal("foreign-user ledger leaked", none, err)
	}
	for _, value := range []any{out, detail, empty, balance, rights} {
		raw, _ := json.Marshal(value)
		for _, secret := range []string{"private-", "secret", "hash", "token", "pubkey", "remark", "idempotency_key", "operator_id", "fingerprint", "register_channel"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("protected storage exposed", secret)
			}
		}
	}
	_, err = f.service.Get(f.ctx, p, 777777)
	approvalCode(t, err, httpx.ErrNotFound)
	_, err = f.service.Ledgers(f.ctx, p, 777777, "balance", 1, 20)
	approvalCode(t, err, httpx.ErrNotFound)
}
func TestManagedUserReadFiltersAndNumericPaginationIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	for i := 10; i < 24; i++ {
		f.user(t, int64(i), fmt.Sprintf("7%09d", i), "BANNED")
		f.write(t, `INSERT INTO balance_ledger(id,user_id,entry_type,amount_micro,balance_after_micro,source_type,source_id,idempotency_key,operator_type,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,'DEPOSIT',1,1,'CHAIN_TRANSFER',$1,$3,'SYSTEM',$4)`, i, f.userID, fmt.Sprintf("page_%d", i), f.clock.Now())
	}
	p := f.people[0]
	first, err := f.service.List(f.ctx, p, ManagedUserFilter{Status: "BANNED", PageSize: 5})
	if err != nil || first.Total != "14" || first.Items[0].ID != "23" || first.Items[4].ID != "19" {
		t.Fatal("numeric user ordering", first, err)
	}
	second, err := f.service.List(f.ctx, p, ManagedUserFilter{Status: "BANNED", Page: 2, PageSize: 5})
	if err != nil || second.Items[0].ID != "18" {
		t.Fatal("second user page", second, err)
	}
	exact, err := f.service.List(f.ctx, p, ManagedUserFilter{AccountNo: "1234567890"})
	if err != nil || exact.Total != "1" || exact.Items[0].ID != strconv.FormatInt(f.userID, 10) {
		t.Fatal("exact account search", exact, err)
	}
	ledgers, err := f.service.Ledgers(f.ctx, p, f.userID, "balance", 2, 5)
	if err != nil || ledgers.Total != "15" {
		t.Fatal(ledgers, err)
	}
	entries := ledgers.Items.([]ManagedBalanceEntry)
	if entries[0].ID != "19" || entries[4].ID != "15" {
		t.Fatal("numeric ledger page", entries)
	}
	beyond, err := f.service.List(f.ctx, p, ManagedUserFilter{Page: 10000, PageSize: 50})
	if err != nil || len(beyond.Items) != 0 || beyond.Total != "16" {
		t.Fatal("last page", beyond, err)
	}
}
func TestManagedUserReadAuthorizationAndReadonlySnapshotIntegration(t *testing.T) {
	for _, mode := range []string{"permission revoked", "ledger permission revoked", "session revoked", "account disabled", "session expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newManagedUserFixture(t)
			p := f.people[0]
			err := f.service.read(f.ctx, p, true, func(ctx context.Context, tx pgx.Tx) error {
				var readonly, isolation string
				if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readonly); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
					return err
				}
				if readonly != "on" || isolation != "repeatable read" {
					t.Fatal("not a readonly consistent snapshot")
				}
				var before, after int64
				if err := tx.QueryRow(ctx, `SELECT available_micro FROM user_balances WHERE user_id=$1`, f.userID).Scan(&before); err != nil {
					return err
				}
				f.write(t, `UPDATE user_balances SET available_micro=0 WHERE user_id=$1`, f.userID)
				if err := tx.QueryRow(ctx, `SELECT available_micro FROM user_balances WHERE user_id=$1`, f.userID).Scan(&after); err != nil {
					return err
				}
				if before != after {
					t.Fatal("inconsistent business snapshot")
				}
				switch mode {
				case "permission revoked":
					f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
				case "ledger permission revoked":
					f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.ledgers.read'`, f.role)
				case "session revoked":
					f.write(t, `UPDATE admin_sessions SET revoked_at=$2 WHERE id=$1`, p.SessionID, f.clock.Now())
				case "account disabled":
					f.write(t, `UPDATE admin_users SET status='DISABLED' WHERE id=$1`, p.ID)
				case "session expired":
					f.clock.Add(sessionTTL)
				}
				return nil
			})
			approvalCode(t, err, httpx.ErrForbidden)
		})
	}
	f := newManagedUserFixture(t)
	cancelled, cancel := context.WithCancel(f.ctx)
	err := f.service.read(cancelled, f.people[0], false, func(ctx context.Context, tx pgx.Tx) error {
		cancel()
		return nil
	})
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read returned success", err)
	}
	if _, err = f.service.Get(f.ctx, f.people[0], f.userID); err != nil {
		t.Fatal("cancelled transaction did not release resources", err)
	}
	err = f.service.read(f.ctx, f.people[0], false, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE user_balances SET available_micro=0 WHERE user_id=$1`, f.userID)
		return err
	})
	if err == nil {
		t.Fatal("readonly transaction permitted mutation")
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM user_balances WHERE available_micro=$1`, int64(math.MaxInt64)); n != 1 {
		t.Fatal("readonly failed to preserve balance")
	}
	// Cached permissions never substitute for fresh database authorization.
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
	_, err = f.service.Get(f.ctx, f.people[0], f.userID)
	approvalCode(t, err, httpx.ErrForbidden)
	_, err = f.service.Get(f.ctx, f.people[0], 777777)
	approvalCode(t, err, httpx.ErrForbidden)
}
func TestManagedUserHTTPStrictQueriesAndReadOnlyRoutesIntegration(t *testing.T) {
	f := newManagedUserFixture(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.RequestIDMiddleware())
	_ = r.SetTrustedProxies(nil)
	h := &Handler{Service: f.svc.Auth, SecureCookies: true}
	g := r.Group("/admin/v1")
	h.Register(g)
	(&UserReadHandler{Auth: h, Service: f.service}).Register(g)
	send := func(method, path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://admin.example/admin/v1"+path, nil)
		req.RemoteAddr = "198.18.0.1:1234"
		req.Header.Set("Origin", "https://admin.example")
		if authenticated {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: f.sessions[0]})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	path := "/users/" + strconv.FormatInt(f.userID, 10)
	if w := send("GET", path, false); w.Code != 401 {
		t.Fatal("missing cookie", w.Code)
	}
	for _, path := range []string{"/users?account_no=1234567890&account_no=5555555555", "/users?page=0", "/users?page=10001", "/users?page_size=51", "/users?account_no=0123456789", "/users?status=LOCKED", path + "?extra=1", path + "/ledgers", path + "/ledgers?type=balance&user_id=5", path + "/ledgers?type=balance&type=entitlement", path + "/ledgers?type=other", "/users/0005", "/users/9223372036854775808"} {
		if w := send("GET", path, true); w.Code != 400 {
			t.Fatal("bad query accepted", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/users", path, path + "/ledgers?type=balance", path + "/ledgers?type=entitlement"} {
		w := send("GET", path, true)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("GET route", path, w.Code, w.Body.String())
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/users", path, path + "/status", path + "/adjustments", path + "/secret-reset"} {
			if w := send(method, path, true); w.Code != 404 && w.Code != 405 {
				t.Fatal("unexpected write route", method, path, w.Code)
			}
		}
	}
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.ledgers.read'`, f.role)
	if w := send("GET", path, true); w.Code != 200 {
		t.Fatal("ledger permission should not gate detail", w.Code)
	}
	if w := send("GET", path+"/ledgers?type=balance", true); w.Code != 403 {
		t.Fatal("ledger permission bypass", w.Code)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM user_balances WHERE available_micro=$1`, int64(math.MaxInt64)); n != 1 {
		t.Fatal("read route mutated balance")
	}
}
