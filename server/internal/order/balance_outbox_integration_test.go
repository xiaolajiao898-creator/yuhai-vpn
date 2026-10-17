package order

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/plan"
)

const orderOutboxPrice int64 = 10_000_000

type orderOutboxClock struct{ now time.Time }

func (c orderOutboxClock) Now() time.Time { return c.now }

type orderOutboxFixture struct {
	ctx                        context.Context
	pool, owner                *pgxpool.Pool
	svc                        *Service
	schema, quoteID, idemKey   string
	userID, planID, opening    int64
	now                        time.Time
	initialBalance, initialSub string
}

// Exercise the real quote, purchase and outbox transactions with app-role
// connections. Each fixture owns an empty random schema: no public balances,
// orders, invitation rules or worker state are changed. LIKE copies table
// columns, identities, checks and indexes; foreign keys/triggers are not copied.
func newOrderOutboxFixture(t *testing.T, opening int64) *orderOutboxFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	appConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid application test database configuration")
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid owner test database configuration")
	}
	if appConfig.ConnConfig.Host != ownerConfig.ConnConfig.Host || appConfig.ConnConfig.Port != ownerConfig.ConnConfig.Port || appConfig.ConnConfig.Database != ownerConfig.ConnConfig.Database {
		t.Fatal("application and owner test databases must match")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	schema := "order_outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
	})
	for _, table := range []string{
		"users", "plans", "plan_quotes", "orders", "user_balances", "balance_ledger",
		"subscriptions", "subscription_ledger", "traffic_packs", "device_addons",
		"receive_addresses", "chain_transfers", "deposit_exceptions", "outbox_events", "system_configs",
	} {
		q := fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", pgx.Identifier{schema, table}.Sanitize(), pgx.Identifier{"public", table}.Sanitize())
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	role := pgx.Identifier{appConfig.ConnConfig.User}.Sanitize()
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + quotedSchema + " TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + quotedSchema + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	// A bounded pool also detects any attempt to borrow another connection
	// while the same request holds its payment transaction and advisory lock.
	appConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	f := &orderOutboxFixture{ctx: ctx, pool: pool, owner: owner, schema: schema, now: now, opening: opening, idemKey: uuid.NewString()}
	f.svc = &Service{Pool: pool, Clock: orderOutboxClock{now: now}}
	accountNo, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	inviteCode, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO users(account_no,secret_lookup,invite_code) VALUES($1,$2,$3) RETURNING id`, accountNo, uuid.NewString(), inviteCode).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO subscriptions(user_id) VALUES($1)`, f.userID)
	f.exec(t, `INSERT INTO user_balances(user_id,available_micro) VALUES($1,$2)`, f.userID, opening)
	if err = pool.QueryRow(ctx, `INSERT INTO plans(code,name,plan_type,tier,duration_days,device_limit,period_quota_bytes,price_micro,status) VALUES('outbox-plan','Fixture monthly plan','TIME',1,30,2,1073741824,$1,'ON_SALE') RETURNING id`, orderOutboxPrice).Scan(&f.planID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT to_jsonb(b)::text,to_jsonb(s)::text FROM user_balances b JOIN subscriptions s USING(user_id) WHERE user_id=$1`, f.userID).Scan(&f.initialBalance, &f.initialSub); err != nil {
		t.Fatal(err)
	}
	quote, err := (&plan.Service{Pool: pool, Clock: orderOutboxClock{now: now}}).Quote(ctx, f.userID, "outbox-plan")
	if err != nil {
		t.Fatal(err)
	}
	if quote.ChangeType != "NEW" || quote.BaseAmount != "10.000000" {
		t.Fatalf("unexpected real plan quote: %+v", quote)
	}
	f.quoteID = quote.QuoteID
	return f
}

func (f *orderOutboxFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *orderOutboxFixture) create() (View, error) {
	return f.svc.Create(f.ctx, f.userID, CreateInput{OrderType: "PLAN", QuoteID: f.quoteID, PayMethod: "BALANCE"}, f.idemKey)
}

func (f *orderOutboxFixture) assertCompletedOnce(t *testing.T, v View) {
	t.Helper()
	if v.OrderNo == "" || v.Status != "COMPLETED" || v.PayMethod != "BALANCE" || v.FulfillmentResult == nil || *v.FulfillmentResult != "FULFILLED" || v.PaidAt == nil || !v.PaidAt.Equal(f.now) || v.CompletedAt == nil || !v.CompletedAt.Equal(f.now) {
		t.Fatalf("unexpected completed order: %+v", v)
	}
	var orders, charges, grants, events int
	var orderID, base, pay int64
	var quoteUsed *int64
	if err := f.pool.QueryRow(f.ctx, `SELECT id,base_amount_micro,pay_amount_micro,(SELECT count(*) FROM orders),(SELECT used_by_order_id FROM plan_quotes WHERE id=$2) FROM orders WHERE order_no=$1`, v.OrderNo, f.quoteID).Scan(&orderID, &base, &pay, &orders, &quoteUsed); err != nil {
		t.Fatal(err)
	}
	if orders != 1 || base != orderOutboxPrice || pay != orderOutboxPrice || quoteUsed == nil || *quoteUsed != orderID {
		t.Fatalf("order or consumed quote mismatch: count=%d base=%d pay=%d quote=%v", orders, base, pay, quoteUsed)
	}
	var available, amount, balanceAfter, chargeSourceID int64
	var chargeType, chargeSource, chargeKey string
	if err := f.pool.QueryRow(f.ctx, `SELECT b.available_micro,l.amount_micro,l.balance_after_micro,l.entry_type,l.source_type,l.source_id,l.idempotency_key,(SELECT count(*) FROM balance_ledger) FROM user_balances b JOIN balance_ledger l USING(user_id) WHERE b.user_id=$1`, f.userID).Scan(&available, &amount, &balanceAfter, &chargeType, &chargeSource, &chargeSourceID, &chargeKey, &charges); err != nil {
		t.Fatal(err)
	}
	if available != f.opening-orderOutboxPrice || amount != -orderOutboxPrice || balanceAfter != available || charges != 1 || chargeType != "PURCHASE" || chargeSource != "ORDER" || chargeSourceID != orderID || chargeKey != "purchase:order:"+strconv.FormatInt(orderID, 10) {
		t.Fatalf("purchase was not applied exactly once: balance=%d amount=%d ledger_after=%d rows=%d type=%s source=%s/%d key=%s", available, amount, balanceAfter, charges, chargeType, chargeSource, chargeSourceID, chargeKey)
	}
	var tier, deviceLimit int
	var subPlanID, grantSourceID, deltaSeconds, quota int64
	var expiry time.Time
	var grantSource, grantKey string
	if err := f.pool.QueryRow(f.ctx, `SELECT s.plan_id,s.tier,s.device_limit,s.expire_at,s.period_quota_bytes,l.source_type,l.source_id,l.idempotency_key,l.delta_seconds,(SELECT count(*) FROM subscription_ledger) FROM subscriptions s JOIN subscription_ledger l USING(user_id) WHERE s.user_id=$1`, f.userID).Scan(&subPlanID, &tier, &deviceLimit, &expiry, &quota, &grantSource, &grantSourceID, &grantKey, &deltaSeconds, &grants); err != nil {
		t.Fatal(err)
	}
	if subPlanID != f.planID || tier != 1 || deviceLimit != 2 || quota != 1<<30 || !expiry.Equal(f.now.Add(30*24*time.Hour)) || grants != 1 || deltaSeconds != 30*86400 || grantSource != "ORDER" || grantSourceID != orderID || grantKey != "fulfill:order:"+strconv.FormatInt(orderID, 10) {
		t.Fatalf("subscription was not granted exactly once: plan=%d tier=%d devices=%d quota=%d expiry=%v rows=%d source=%s/%d seconds=%d", subPlanID, tier, deviceLimit, quota, expiry, grants, grantSource, grantSourceID, deltaSeconds)
	}
	var eventType, aggregateType, status, payload string
	var aggregateID int64
	var attempts int
	if err := f.pool.QueryRow(f.ctx, `SELECT event_type,aggregate_type,aggregate_id,status,attempts,payload::text,(SELECT count(*) FROM outbox_events) FROM outbox_events`).Scan(&eventType, &aggregateType, &aggregateID, &status, &attempts, &payload, &events); err != nil {
		t.Fatal(err)
	}
	if events != 1 || eventType != "ORDER_COMPLETED" || aggregateType != "order" || aggregateID != orderID || status != "PENDING" || attempts != 0 || payload != "{}" {
		t.Fatalf("completion event mismatch: rows=%d type=%s aggregate=%s/%d status=%s attempts=%d payload=%s", events, eventType, aggregateType, aggregateID, status, attempts, payload)
	}
}

func (f *orderOutboxFixture) assertRolledBack(t *testing.T) {
	t.Helper()
	var orders, charges, grants, events int
	var used *int64
	var currentBalance, currentSub string
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM balance_ledger),(SELECT count(*) FROM subscription_ledger),(SELECT count(*) FROM outbox_events),(SELECT used_by_order_id FROM plan_quotes WHERE id=$1)`, f.quoteID).Scan(&orders, &charges, &grants, &events, &used); err != nil {
		t.Fatal(err)
	}
	if orders != 0 || charges != 0 || grants != 0 || events != 0 || used != nil {
		t.Fatalf("transaction partially committed: orders=%d charges=%d grants=%d events=%d used_quote=%v", orders, charges, grants, events, used)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT to_jsonb(b)::text,to_jsonb(s)::text FROM user_balances b JOIN subscriptions s USING(user_id) WHERE user_id=$1`, f.userID).Scan(&currentBalance, &currentSub); err != nil {
		t.Fatal(err)
	}
	if currentBalance != f.initialBalance || currentSub != f.initialSub {
		t.Fatalf("rollback changed balance or subscription snapshot: balance=%s subscription=%s", currentBalance, currentSub)
	}
}

func TestBalanceOrderCompletionOutboxReplayIntegration(t *testing.T) {
	f := newOrderOutboxFixture(t, 25_000_000)
	first, err := f.create()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		replay, err := f.create()
		if err != nil || replay.OrderNo != first.OrderNo {
			t.Fatalf("idempotent replay changed result: order=%s err=%v", replay.OrderNo, err)
		}
	}
	f.assertCompletedOnce(t, first)
}

func TestBalanceOrderCompletionOutboxConcurrentIntegration(t *testing.T) {
	f := newOrderOutboxFixture(t, 25_000_000)
	const callers = 8
	type result struct {
		view View
		err  error
	}
	results := make(chan result, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			view, err := f.create()
			results <- result{view: view, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var first View
	for r := range results {
		if r.err != nil {
			t.Errorf("concurrent same-key create must return the completed order: %v", r.err)
			continue
		}
		if first.OrderNo == "" {
			first = r.view
		}
		if r.view.OrderNo != first.OrderNo {
			t.Errorf("concurrent retry returned a different order: %s != %s", r.view.OrderNo, first.OrderNo)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	f.assertCompletedOnce(t, first)
}

func TestBalanceOrderInsufficientFundsRollsBackCompletionIntegration(t *testing.T) {
	f := newOrderOutboxFixture(t, orderOutboxPrice-1)
	_, err := f.create()
	var businessErr *httpx.Error
	if !errors.As(err, &businessErr) || businessErr.Code != httpx.ErrInsufficientFunds {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
	f.assertRolledBack(t)
}

func TestBalanceOrderOutboxFailureRollsBackPaymentAndGrantIntegration(t *testing.T) {
	f := newOrderOutboxFixture(t, 25_000_000)
	table := pgx.Identifier{f.schema, "outbox_events"}.Sanitize()
	// Fail the final event insertion after the purchase ledger and grant have
	// been written, without replacing any production service implementation.
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+table+" ADD CONSTRAINT reject_fixture_completion CHECK (event_type <> 'ORDER_COMPLETED')"); err != nil {
		t.Fatal(err)
	}
	_, err := f.create()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "reject_fixture_completion" {
		t.Fatalf("expected injected outbox failure after fulfillment, got %v", err)
	}
	f.assertRolledBack(t)
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+table+" DROP CONSTRAINT reject_fixture_completion"); err != nil {
		t.Fatal(err)
	}
	// The original quote and idempotency key must remain reusable after the
	// rollback; success then leaves exactly one charge, grant and pending event.
	retry, err := f.create()
	if err != nil {
		t.Fatal(err)
	}
	f.assertCompletedOnce(t, retry)
}
