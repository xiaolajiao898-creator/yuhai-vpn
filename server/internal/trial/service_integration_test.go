package trial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type fixture struct {
	ctx         context.Context
	pool, owner *pgxpool.Pool
	svc         *Service
	schema      string
	userID      int64
	deviceID    int64
	now         time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	appConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	schema := "trial_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quoted := pgx.Identifier{schema}.Sanitize()
	ownerConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = owner.Exec(c, "DROP SCHEMA "+quoted+" CASCADE")
	})
	for _, table := range []string{"users", "devices", "subscriptions", "subscription_ledger", "system_configs", "trial_grants"} {
		q := fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", pgx.Identifier{schema, table}.Sanitize(), pgx.Identifier{"public", table}.Sanitize())
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(table, err)
		}
	}
	role := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + quoted + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON " + pgx.Identifier{schema, "users"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT ON " + pgx.Identifier{schema, "devices"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON " + pgx.Identifier{schema, "subscriptions"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT ON " + pgx.Identifier{schema, "subscription_ledger"}.Sanitize() + " TO " + role,
		"GRANT SELECT ON " + pgx.Identifier{schema, "system_configs"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT ON " + pgx.Identifier{schema, "trial_grants"}.Sanitize() + " TO " + role,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	if _, err = owner.Exec(ctx, `INSERT INTO `+pgx.Identifier{schema, "system_configs"}.Sanitize()+`(key,value) VALUES('trial.enabled','true'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	appConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
	f := &fixture{ctx: ctx, pool: pool, owner: owner, schema: schema, now: now, svc: &Service{Pool: pool, Clock: fixedClock{now}}}
	f.userID, f.deviceID = f.insertUserDevice(t, strings.Repeat("ab", 32))
	return f
}

func (f *fixture) insertUserDevice(t *testing.T, fingerprint string) (int64, int64) {
	t.Helper()
	account, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	var userID, deviceID int64
	secret := strings.ReplaceAll(uuid.NewString(), "-", "") + strings.ReplaceAll(uuid.NewString(), "-", "")[:32]
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO users(account_no,secret_lookup,invite_code) VALUES($1,$2,$3) RETURNING id`, account, secret, invite).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO devices(user_id,device_uid,pubkey,platform,fingerprint_hash) VALUES($1,$2,$3,'ANDROID',NULLIF($4,'')) RETURNING id`,
		userID, uuid.NewString(), uuid.NewString(), fingerprint).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	return userID, deviceID
}

func (f *fixture) setEnabled(t *testing.T, on bool) {
	t.Helper()
	raw := "false"
	if on {
		raw = "true"
	}
	if _, err := f.owner.Exec(f.ctx, `UPDATE `+pgx.Identifier{f.schema, "system_configs"}.Sanitize()+` SET value=$1::jsonb WHERE key='trial.enabled'`, raw); err != nil {
		t.Fatal(err)
	}
}

func TestClaimIsIdempotentAndDoesNotExtend(t *testing.T) {
	f := newFixture(t)
	install := uuid.NewString()
	first, err := f.svc.Claim(f.ctx, f.userID, f.deviceID, install)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "granted" || !first.Active || first.Tier != 1 || first.DeviceLimit != 3 || !first.UnlimitedTraffic {
		t.Fatalf("%+v", first)
	}
	if !first.ExpireAt.Equal(f.now.Add(24 * time.Hour)) {
		t.Fatalf("expire %s", first.ExpireAt)
	}
	f.svc.Clock = fixedClock{f.now.Add(time.Hour)}
	second, err := f.svc.Claim(f.ctx, f.userID, f.deviceID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "already" || !second.ExpireAt.Equal(first.ExpireAt) {
		t.Fatalf("extended or not idempotent: %+v", second)
	}
	var ledger int
	var tier, devices int
	var quota *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM subscription_ledger WHERE idempotency_key=$1), s.tier, s.device_limit, s.period_quota_bytes FROM subscriptions s WHERE s.user_id=$2`,
		"trial:user:"+itoa(f.userID), f.userID).Scan(&ledger, &tier, &devices, &quota); err != nil {
		t.Fatal(err)
	}
	if ledger != 1 || tier != 1 || devices != 3 || quota != nil {
		t.Fatalf("ledger=%d tier=%d devices=%d quota=%v", ledger, tier, devices, quota)
	}
	view, err := f.svc.View(f.ctx, f.userID, f.deviceID)
	if err != nil || view.Status != "active" || view.Eligible {
		t.Fatalf("%+v %v", view, err)
	}
}

func TestClaimRejectsReuseAndPriorEntitlement(t *testing.T) {
	f := newFixture(t)
	install := uuid.NewString()
	if _, err := f.svc.Claim(f.ctx, f.userID, f.deviceID, install); err != nil {
		t.Fatal(err)
	}
	otherUser, otherDevice := f.insertUserDevice(t, "")
	_, err := f.svc.Claim(f.ctx, otherUser, otherDevice, install)
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) || httpErr.Code != httpx.ErrConflict || httpErr.Details["reason"] != "DEVICE_USED" {
		t.Fatalf("install reuse: %v", err)
	}
	fpUser, fpDevice := f.insertUserDevice(t, strings.Repeat("ab", 32))
	_, err = f.svc.Claim(f.ctx, fpUser, fpDevice, uuid.NewString())
	if !errors.As(err, &httpErr) || httpErr.Details["reason"] != "DEVICE_USED" {
		t.Fatalf("fingerprint reuse: %v", err)
	}
	paidUser, paidDevice := f.insertUserDevice(t, strings.Repeat("cd", 32))
	if _, err = f.pool.Exec(f.ctx, `INSERT INTO subscription_ledger(user_id,change_type,source_type,delta_seconds,before_snapshot,after_snapshot,idempotency_key,operator_type) VALUES($1,'PURCHASE','ORDER',3600,'{}','{}',$2,'SYSTEM')`, paidUser, "paid:"+itoa(paidUser)); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Claim(f.ctx, paidUser, paidDevice, uuid.NewString())
	if !errors.As(err, &httpErr) || httpErr.Details["reason"] != "NOT_NEW" {
		t.Fatalf("paid account: %v", err)
	}
	f.setEnabled(t, false)
	freshUser, freshDevice := f.insertUserDevice(t, "")
	_, err = f.svc.Claim(f.ctx, freshUser, freshDevice, uuid.NewString())
	if !errors.As(err, &httpErr) || httpErr.Code != httpx.ErrTrialDisabled {
		t.Fatalf("disabled: %v", err)
	}
	view, err := f.svc.View(f.ctx, freshUser, freshDevice)
	if err != nil || view.Enabled || view.Status != "disabled" {
		t.Fatalf("%+v %v", view, err)
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
