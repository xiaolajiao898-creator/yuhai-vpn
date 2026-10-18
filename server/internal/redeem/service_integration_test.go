package redeem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type redeemFixture struct {
	ctx         context.Context
	pool, owner *pgxpool.Pool
	svc         *Service
	schema      string
	userID      int64
	now         time.Time
	cipher      *fieldenc.Cipher
}

func newRedeemFixture(t *testing.T) *redeemFixture {
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
	schema := "redeem_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
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
	for _, table := range []string{"users", "subscriptions", "subscription_ledger", "redeem_card_types", "redeem_batches", "redeem_codes"} {
		q := fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", pgx.Identifier{schema, table}.Sanitize(), pgx.Identifier{"public", table}.Sanitize())
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(table, err)
		}
	}
	role := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	codes := pgx.Identifier{schema, "redeem_codes"}.Sanitize()
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + quoted + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON " + pgx.Identifier{schema, "users"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON " + pgx.Identifier{schema, "subscriptions"}.Sanitize() + " TO " + role,
		"GRANT SELECT, INSERT ON " + pgx.Identifier{schema, "subscription_ledger"}.Sanitize() + " TO " + role,
		"GRANT SELECT ON " + pgx.Identifier{schema, "redeem_card_types"}.Sanitize() + " TO " + role,
		"REVOKE ALL ON " + codes + " FROM " + role,
		"GRANT SELECT (id, code_hash, batch_id, card_type, duration_days, tier, device_limit, traffic_bytes, status, redeemed_by, redeemed_at) ON " + codes + " TO " + role,
		"GRANT UPDATE (status, redeemed_by, redeemed_at) ON " + codes + " TO " + role,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	if _, err = owner.Exec(ctx, `INSERT INTO `+pgx.Identifier{schema, "redeem_card_types"}.Sanitize()+`
		(code,name,duration_days,tier,device_limit,traffic_bytes) VALUES
		('WEEK','周卡',7,1,3,NULL),('MONTH','月卡',30,1,3,NULL),('YEAR','年卡',365,1,3,NULL)`); err != nil {
		t.Fatal(err)
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	appConfig.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{7}, 32)
	cipher, err := fieldenc.New("redeem-test", key)
	if err != nil {
		t.Fatal(err)
	}
	f := &redeemFixture{ctx: ctx, pool: pool, owner: owner, schema: schema, now: now, cipher: cipher}
	f.svc = &Service{Pool: pool, Clock: fixedClock{now}}
	f.userID = f.insertUser(t)
	return f
}

func (f *redeemFixture) insertUser(t *testing.T) int64 {
	t.Helper()
	account, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO users(account_no,secret_lookup,invite_code) VALUES($1,$2,$3) RETURNING id`,
		account, strings.ReplaceAll(uuid.NewString(), "-", "")+strings.ReplaceAll(uuid.NewString(), "-", "")[:32], invite).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *redeemFixture) batch(t *testing.T, card string, n int, note string) Batch {
	t.Helper()
	var out Batch
	err := pgx.BeginFunc(f.ctx, f.owner, func(tx pgx.Tx) error {
		b, err := CreateBatch(f.ctx, tx, BatchInput{CardType: card, Quantity: n, Note: note, AdminID: 1, Now: f.now}, func(id int64, codes []string) ([]byte, error) {
			return SealCodes(f.cipher, id, codes)
		})
		out = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func codeOf(err error) string {
	var e *httpx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestRedeemSuccessDoubleRevokedAndUnknown(t *testing.T) {
	f := newRedeemFixture(t)
	batch := f.batch(t, "WEEK", 2, "给张三")
	if len(batch.Codes) != 2 || batch.Codes[0] == batch.Codes[1] {
		t.Fatal(batch.Codes)
	}
	var stored string
	var blob []byte
	if err := f.owner.QueryRow(f.ctx, `SELECT code_hash::text, (SELECT codes_enc FROM redeem_batches WHERE id=$1) FROM redeem_codes WHERE batch_id=$1 LIMIT 1`, batch.ID).Scan(&stored, &blob); err != nil {
		t.Fatal(err)
	}
	for _, code := range batch.Codes {
		norm, _ := Normalize(code)
		if stored == code || stored == norm || bytes.Contains(blob, []byte(code)) || bytes.Contains(blob, []byte(norm)) {
			t.Fatal("plaintext stored")
		}
	}
	opened, err := OpenCodes(f.cipher, batch.ID, blob)
	if err != nil || len(opened) != 2 {
		t.Fatal(opened, err)
	}

	got, err := f.svc.Redeem(f.ctx, f.userID, strings.ToLower(batch.Codes[0]))
	if err != nil {
		t.Fatal(err)
	}
	wantExpire := f.now.Add(7 * 24 * time.Hour)
	if got.Extended || got.CardName != "周卡" || got.Tier != 1 || got.DeviceLimit != 3 || !got.Unlimited || !got.ExpireAt.Equal(wantExpire) {
		t.Fatalf("%+v", got)
	}
	var status string
	var ledger int
	var tier, devices int
	var quota *int64
	var expire time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT c.status, (SELECT count(*) FROM subscription_ledger WHERE idempotency_key='redeem:'||c.id), s.tier, s.device_limit, s.period_quota_bytes, s.expire_at FROM redeem_codes c JOIN subscriptions s ON s.user_id=$2 WHERE c.code_hash=$1`, Hash(mustNorm(batch.Codes[0])), f.userID).Scan(&status, &ledger, &tier, &devices, &quota, &expire); err != nil {
		t.Fatal(err)
	}
	if status != "used" || ledger != 1 || tier != 1 || devices != 3 || quota != nil || !expire.Equal(wantExpire) {
		t.Fatalf("status=%s ledger=%d tier=%d devices=%d quota=%v expire=%s", status, ledger, tier, devices, quota, expire)
	}

	_, err = f.svc.Redeem(f.ctx, f.userID, batch.Codes[0])
	if codeOf(err) != "已使用" {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(f.ctx, `SELECT count(*) FROM subscription_ledger WHERE user_id=$1`, f.userID).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatal(ledger, err)
	}

	revoked := f.batch(t, "MONTH", 1, "")
	if _, err = f.owner.Exec(f.ctx, `UPDATE redeem_codes SET status='revoked', revoked_by=1, revoked_at=$2 WHERE batch_id=$1`, revoked.ID, f.now); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Redeem(f.ctx, f.userID, revoked.Codes[0])
	if codeOf(err) != "已作废" {
		t.Fatal(err)
	}
	_, err = f.svc.Redeem(f.ctx, f.userID, "ABCD-EFGH-JKLM-NPQR")
	if codeOf(err) != "码不存在" {
		t.Fatal(err)
	}
}

func TestRedeemStackingKeepsHigherTierAndExtends(t *testing.T) {
	f := newRedeemFixture(t)
	oldExpire := f.now.Add(10 * 24 * time.Hour)
	quota := int64(1000)
	planID := int64(1)
	// plan_id 没有外键（LIKE 不复制外键），用来确认「只续期」时会留下原来的套餐编号。
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO subscriptions(user_id,plan_id,tier,expire_at,device_limit,period_quota_bytes,period_used_bytes,started_at) VALUES($1,$2,3,$3,5,$4,10,$5)`, f.userID, planID, oldExpire, quota, f.now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	batch := f.batch(t, "WEEK", 1, "续上")
	got, err := f.svc.Redeem(f.ctx, f.userID, batch.Codes[0])
	if err != nil {
		t.Fatal(err)
	}
	want := oldExpire.Add(7 * 24 * time.Hour)
	if !got.Extended || got.Tier != 3 || got.DeviceLimit != 5 || !got.Unlimited || !got.ExpireAt.Equal(want) {
		t.Fatalf("stack %+v want expire %s", got, want)
	}
	var kept *int64
	var tier, devices int
	var afterQuota *int64
	var expire time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT plan_id, tier, device_limit, period_quota_bytes, expire_at FROM subscriptions WHERE user_id=$1`, f.userID).Scan(&kept, &tier, &devices, &afterQuota, &expire); err != nil {
		t.Fatal(err)
	}
	// 流量从不限额度被抬成不限，套餐名不能再冒充原来的付费套餐，所以 plan_id 清空。等级和设备数保持更高的那个。
	if kept != nil || tier != 3 || devices != 5 || afterQuota != nil || !expire.Equal(want) {
		t.Fatalf("plan=%v tier=%d devices=%d quota=%v expire=%s", kept, tier, devices, afterQuota, expire)
	}

	// 已经不限流量、等级和设备都不低于卡时，只加天数，套餐编号保留。
	other := f.insertUser(t)
	if _, err = f.owner.Exec(f.ctx, `INSERT INTO subscriptions(user_id,plan_id,tier,expire_at,device_limit,started_at) VALUES($1,$2,1,$3,3,$4)`, other, planID, oldExpire, f.now); err != nil {
		t.Fatal(err)
	}
	year := f.batch(t, "YEAR", 1, "")
	got, err = f.svc.Redeem(f.ctx, other, year.Codes[0])
	if err != nil {
		t.Fatal(err)
	}
	want = oldExpire.Add(365 * 24 * time.Hour)
	if !got.Extended || !got.ExpireAt.Equal(want) || got.Tier != 1 || got.DeviceLimit != 3 {
		t.Fatalf("extend only %+v", got)
	}
	if err = f.pool.QueryRow(f.ctx, `SELECT plan_id, expire_at FROM subscriptions WHERE user_id=$1`, other).Scan(&kept, &expire); err != nil || kept == nil || *kept != planID || !expire.Equal(want) {
		t.Fatal(kept, expire, err)
	}
}

func TestRedeemExpiredPlanStartsFromNow(t *testing.T) {
	f := newRedeemFixture(t)
	past := f.now.Add(-48 * time.Hour)
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO subscriptions(user_id,tier,expire_at,device_limit,started_at) VALUES($1,5,$2,9,$3)`, f.userID, past, past); err != nil {
		t.Fatal(err)
	}
	batch := f.batch(t, "MONTH", 1, "")
	got, err := f.svc.Redeem(f.ctx, f.userID, batch.Codes[0])
	if err != nil {
		t.Fatal(err)
	}
	want := f.now.Add(30 * 24 * time.Hour)
	if got.Extended || got.Tier != 1 || got.DeviceLimit != 3 || !got.ExpireAt.Equal(want) {
		t.Fatalf("expired plan should be replaced %+v", got)
	}
}

func TestApplyRedeemIsIdempotent(t *testing.T) {
	f := newRedeemFixture(t)
	batch := f.batch(t, "WEEK", 1, "")
	var id int64
	if err := f.owner.QueryRow(f.ctx, `SELECT id FROM redeem_codes WHERE batch_id=$1`, batch.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	err := pgx.BeginFunc(f.ctx, f.owner, func(tx pgx.Tx) error {
		first, err := entitlement.ApplyRedeem(f.ctx, tx, entitlement.RedeemGrant{UserID: f.userID, CodeID: id, DurationDays: 7, Tier: 1, DeviceLimit: 3, Remark: "兑换周卡"}, f.now)
		if err != nil {
			return err
		}
		second, err := entitlement.ApplyRedeem(f.ctx, tx, entitlement.RedeemGrant{UserID: f.userID, CodeID: id, DurationDays: 7, Tier: 1, DeviceLimit: 3, Remark: "兑换周卡"}, f.now)
		if err != nil {
			return err
		}
		if first.AlreadyApplied || !second.AlreadyApplied || !first.ExpireAt.Equal(second.ExpireAt) {
			return fmt.Errorf("idempotency %+v %+v", first, second)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var expire time.Time
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*), max(expire_at) FROM subscription_ledger l JOIN subscriptions s USING (user_id) WHERE l.user_id=$1`, f.userID).Scan(&n, &expire); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !expire.Equal(f.now.Add(7*24*time.Hour)) {
		t.Fatalf("ledger=%d expire=%s", n, expire)
	}
}

func TestConcurrentRedeemUsesCodeOnce(t *testing.T) {
	f := newRedeemFixture(t)
	batch := f.batch(t, "WEEK", 1, "")
	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.svc.Redeem(f.ctx, f.userID, batch.Codes[0])
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		if codeOf(err) != "已使用" {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("successes=%d errs=%v", ok, errs)
	}
	var ledger int
	var expire time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*), max(s.expire_at) FROM subscription_ledger l JOIN subscriptions s USING (user_id) WHERE l.user_id=$1`, f.userID).Scan(&ledger, &expire); err != nil {
		t.Fatal(err)
	}
	if ledger != 1 || !expire.Equal(f.now.Add(7*24*time.Hour)) {
		t.Fatalf("ledger=%d expire=%s", ledger, expire)
	}
}

func TestCreateBatchTermsComeFromTable(t *testing.T) {
	f := newRedeemFixture(t)
	if _, err := f.owner.Exec(f.ctx, `UPDATE redeem_card_types SET duration_days=8 WHERE code='WEEK'`); err != nil {
		t.Fatal(err)
	}
	batch := f.batch(t, "week", 3, " 渠道甲 ")
	if batch.Days != 8 || batch.CardName != "周卡" || batch.Note != "渠道甲" || len(batch.Codes) != 3 {
		t.Fatalf("%+v", batch)
	}
	seen := map[string]struct{}{}
	for _, code := range batch.Codes {
		if _, ok := seen[code]; ok {
			t.Fatal("duplicate in batch")
		}
		seen[code] = struct{}{}
	}
	var days int
	if err := f.owner.QueryRow(f.ctx, `SELECT duration_days FROM redeem_codes WHERE batch_id=$1 LIMIT 1`, batch.ID).Scan(&days); err != nil || days != 8 {
		t.Fatal(days, err)
	}
	err := pgx.BeginFunc(f.ctx, f.owner, func(tx pgx.Tx) error {
		_, err := CreateBatch(f.ctx, tx, BatchInput{CardType: "DECADE", Quantity: 1, AdminID: 1, Now: f.now}, func(int64, []string) ([]byte, error) {
			return []byte{1}, nil
		})
		return err
	})
	if codeOf(err) != "卡类型不存在" {
		t.Fatal(err)
	}
}

func mustNorm(code string) string {
	n, err := Normalize(code)
	if err != nil {
		panic(err)
	}
	return n
}
