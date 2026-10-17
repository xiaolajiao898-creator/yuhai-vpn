package invite

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/balance"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/money"
	"github.com/yuhai-vpn/server/internal/outbox"
)

type rewardFixture struct {
	ctx                       context.Context
	pool, owner               *pgxpool.Pool
	svc                       *RewardService
	schema                    string
	now                       time.Time
	inviter, invitee, address int64
}

// Every table, rule and accounting entry is isolated in a random test schema.
// No enabled rule or fixture balance is ever added to the public application.
// Multiple real app-role connections still exercise locking and SKIP LOCKED.
func newRewardFixture(t *testing.T) *rewardFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	a, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("bad application test configuration")
	}
	o, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("bad owner test configuration")
	}
	if a.ConnConfig.Host != o.ConnConfig.Host || a.ConnConfig.Port != o.ConnConfig.Port || a.ConnConfig.Database != o.ConnConfig.Database {
		t.Fatal("test databases must match")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.NewWithConfig(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	schema := "reward_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := owner.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Errorf("fixture cleanup: %v", e)
		}
	})
	for _, table := range []string{"users", "devices", "device_credentials", "invite_relations", "invite_reward_rules", "invite_rewards", "shared_pay_addresses", "blacklist", "orders", "chain_transfers", "receive_addresses", "deposit_exceptions", "user_balances", "balance_ledger", "subscriptions", "subscription_ledger", "plans", "traffic_packs", "system_configs", "node_user_events"} {
		q := fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING ALL)", pgx.Identifier{schema, table}.Sanitize(), pgx.Identifier{"public", table}.Sanitize())
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	role := pgx.Identifier{a.ConnConfig.User}.Sanitize()
	for _, q := range []string{"GRANT USAGE ON SCHEMA " + quoted + " TO " + role, "GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + quoted + " TO " + role, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	a.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	f := &rewardFixture{ctx: ctx, pool: pool, owner: owner, schema: schema, now: now, svc: &RewardService{Pool: pool, Clock: fixedClock{now}}}
	f.inviter = f.user(t)
	f.invitee = f.user(t)
	f.exec(t, `INSERT INTO invite_relations(invitee_user_id,inviter_user_id,invite_code,bind_method,created_at) VALUES($1,$2,'TEST234','MANUAL',$3)`, f.invitee, f.inviter, now.Add(-12*time.Hour))
	if err = pool.QueryRow(ctx, `INSERT INTO receive_addresses(network,address,signature,sig_key_id,valid_until,status) VALUES('TRON','fixture-recipient','test','test',$1,'ACTIVE') RETURNING id`, now.AddDate(1, 0, 0)).Scan(&f.address); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *rewardFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *rewardFixture) user(t *testing.T) int64 {
	t.Helper()
	account, _ := ids.NewAccountNo()
	code, _ := ids.NewInviteCode()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO users(account_no,secret_lookup,invite_code,created_at) VALUES($1,$2,$3,$4) RETURNING id`, account, uuid.NewString(), code, f.now.Add(-24*time.Hour)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO subscriptions(user_id) VALUES($1)`, id)
	f.exec(t, `INSERT INTO user_balances(user_id) VALUES($1)`, id)
	return id
}
func (f *rewardFixture) rule(t *testing.T, trigger, kind string, value int64, freeze int, cap *int, percent *int) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `INSERT INTO invite_reward_rules(name,trigger_event,beneficiary,reward_type,reward_value,reward_percent_bp,freeze_hours,daily_cap_per_inviter,status,created_at) VALUES('test',$1,'INVITER',$2,$3,$4,$5,$6,'ENABLED',$7) RETURNING id`, trigger, kind, value, percent, freeze, cap, f.now.Add(-24*time.Hour)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *rewardFixture) order(t *testing.T, paid time.Time) int64 {
	t.Helper()
	var id, transfer int64
	err := rewardTransaction(f.ctx, f.pool, func(tx pgx.Tx) error {
		if e := tx.QueryRow(f.ctx, `INSERT INTO orders(order_no,user_id,order_type,base_amount_micro,pay_amount_micro,pay_method,receive_address_id,status,fulfillment_result,client_idem_key,expires_at,paid_at,completed_at,created_at) VALUES($1,$2,'TOPUP',10000000,10000100,'USDT_TRC20',$3,'COMPLETED','FULFILLED',$4,$5,$6,$7,$8) RETURNING id`, uuid.NewString()[:24], f.invitee, f.address, uuid.NewString(), f.now.Add(time.Hour), paid, paid.Add(time.Minute), paid.Add(-time.Minute)).Scan(&id); e != nil {
			return e
		}
		if e := tx.QueryRow(f.ctx, `INSERT INTO chain_transfers(network,tx_hash,contract_address,from_address,to_address,receive_address_id,amount_micro,block_number,block_time,confirm_status,match_status,order_id,data_source) VALUES('TRON',$1,'fixture-contract','fixture-payer','fixture-recipient',$2,10000100,1,$3,'SOLIDIFIED','MATCHED',$4,'FULLNODE') RETURNING id`, uuid.NewString(), f.address, paid, id).Scan(&transfer); e != nil {
			return e
		}
		if _, e := tx.Exec(f.ctx, `UPDATE orders SET chain_transfer_id=$2 WHERE id=$1`, id, transfer); e != nil {
			return e
		}
		_, e := balance.Apply(f.ctx, tx, balance.Entry{UserID: f.invitee, EntryType: "DEPOSIT", Amount: 10000100, SourceType: "CHAIN_TRANSFER", SourceID: transfer, IdemKey: "fixture:deposit:" + strconv.FormatInt(transfer, 10), Operator: "SYSTEM"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *rewardFixture) event(id int64) outbox.Event {
	return outbox.Event{Type: outbox.OrderCompleted, AggregateType: "order", AggregateID: id, Payload: []byte(`{"user_id":999999,"amount_micro":9223372036854775807}`)}
}
func (f *rewardFixture) state(t *testing.T) (int, string, int64) {
	t.Helper()
	var n int
	var status string
	var value int64
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*),COALESCE(min(status),''),COALESCE(sum(reward_value),0) FROM invite_rewards`).Scan(&n, &status, &value); err != nil {
		t.Fatal(err)
	}
	return n, status, value
}
func (f *rewardFixture) assertNoGrant(t *testing.T) {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM balance_ledger WHERE entry_type='INVITE_REWARD')+(SELECT count(*) FROM subscription_ledger WHERE source_type='INVITE_REWARD')`).Scan(&n); err != nil || n != 0 {
		t.Fatal("unexpected reward grant", n, err)
	}
}
func runRewardConcurrent(t *testing.T, n int, fn func() error) {
	t.Helper()
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errCh <- fn() }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRewardFirstPaymentReplayFreezeAndConcurrentReleaseIntegration(t *testing.T) {
	f := newRewardFixture(t)
	f.rule(t, "INVITEE_FIRST_PAID", "BALANCE", 2000000, 72, nil, nil)
	first := f.order(t, f.now.Add(-2*time.Hour))
	second := f.order(t, f.now.Add(-time.Hour))
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(second)); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := f.state(t); n != 0 {
		t.Fatal("out-of-order event stole first-payment reward")
	}
	runRewardConcurrent(t, 8, func() error { return f.svc.OnOrderCompleted(f.ctx, f.event(first)) })
	if n, status, value := f.state(t); n != 1 || status != "PENDING" || value != 2000000 {
		t.Fatal(n, status, value)
	}
	f.assertNoGrant(t)
	due := f.now.Add(72 * time.Hour)
	if err := f.svc.ReleaseDue(f.ctx, due.Add(-time.Microsecond)); err != nil {
		t.Fatal(err)
	}
	f.assertNoGrant(t)
	runRewardConcurrent(t, 8, func() error { return f.svc.ReleaseDue(f.ctx, due) })
	if n, status, _ := f.state(t); n != 1 || status != "RELEASED" {
		t.Fatal(n, status)
	}
	var available int64
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT available_micro,(SELECT count(*) FROM balance_ledger WHERE entry_type='INVITE_REWARD') FROM user_balances WHERE user_id=$1`, f.inviter).Scan(&available, &count); err != nil || available != 2000000 || count != 1 {
		t.Fatal(available, count, err)
	}
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(first)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReleaseDue(f.ctx, due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestRewardGrantAndStateRollbackTogetherIntegration(t *testing.T) {
	f := newRewardFixture(t)
	f.rule(t, "INVITEE_FIRST_PAID", "BALANCE", 500000, 0, nil, nil)
	order := f.order(t, f.now.Add(-time.Hour))
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(order)); err != nil {
		t.Fatal(err)
	}
	table := pgx.Identifier{f.schema, "invite_rewards"}.Sanitize()
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+table+" ADD CONSTRAINT reject_fixture_release CHECK(status<>'RELEASED')"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReleaseDue(f.ctx, f.now); err == nil {
		t.Fatal("expected state-update failure")
	}
	f.assertNoGrant(t)
	if _, status, _ := f.state(t); status != "PENDING" {
		t.Fatal("failed release lost pending reward", status)
	}
	var amount int64
	if err := f.pool.QueryRow(f.ctx, `SELECT available_micro FROM user_balances WHERE user_id=$1`, f.inviter).Scan(&amount); err != nil || amount != 0 {
		t.Fatal("partial balance commit", amount, err)
	}
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+table+" DROP CONSTRAINT reject_fixture_release"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ReleaseDue(f.ctx, f.now); err != nil {
		t.Fatal(err)
	}
	if _, status, _ := f.state(t); status != "RELEASED" {
		t.Fatal(status)
	}
}

func TestRewardRechecksFrozenOrderAndBlacklistIntegration(t *testing.T) {
	for _, kind := range []string{"blacklist", "account-number blacklist", "failed payment", "changed amount"} {
		t.Run(kind, func(t *testing.T) {
			f := newRewardFixture(t)
			f.rule(t, "INVITEE_FIRST_PAID", "BALANCE", 1000, 1, nil, nil)
			order := f.order(t, f.now.Add(-time.Hour))
			if err := f.svc.OnOrderCompleted(f.ctx, f.event(order)); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "blacklist":
				f.exec(t, `INSERT INTO blacklist(target_type,target_value,effects,reason,created_by) VALUES('USER',$1,ARRAY['INVITE_REWARD'],'test',1)`, strconv.FormatInt(f.inviter, 10))
			case "account-number blacklist":
				f.exec(t, `INSERT INTO blacklist(target_type,target_value,effects,reason,created_by) SELECT 'USER',account_no,ARRAY['INVITE_REWARD'],'test',1 FROM users WHERE id=$1`, f.inviter)
			case "failed payment":
				f.exec(t, `UPDATE chain_transfers SET confirm_status='FAILED' WHERE order_id=$1`, order)
			case "changed amount":
				f.exec(t, `UPDATE orders SET base_amount_micro=9000000 WHERE id=$1`, order)
			}
			if err := f.svc.ReleaseDue(f.ctx, f.now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, status, _ := f.state(t); status != "CANCELLED" {
				t.Fatal("frozen risk was not cancelled", kind, status)
			}
			f.assertNoGrant(t)
		})
	}
}

func TestRewardRiskSignalsAndMissingConfigurationAreNotGrantedIntegration(t *testing.T) {
	for _, signal := range []string{"same fingerprint", "same public key", "same prefix", "public payer", "mock payment", "missing days plan", "missing traffic plan", "uncapped each payment"} {
		t.Run(signal, func(t *testing.T) {
			f := newRewardFixture(t)
			kind, trigger := "BALANCE", "INVITEE_FIRST_PAID"
			if signal == "missing days plan" {
				kind = "DAYS"
			}
			if signal == "missing traffic plan" {
				kind = "TRAFFIC"
			}
			if signal == "uncapped each payment" {
				trigger = "INVITEE_PAID"
			}
			f.rule(t, trigger, kind, 7, 0, nil, nil)
			order := f.order(t, f.now.Add(-time.Hour))
			switch signal {
			case "same fingerprint":
				for _, u := range []int64{f.inviter, f.invitee} {
					f.exec(t, `INSERT INTO devices(user_id,device_uid,pubkey,platform,fingerprint_hash) VALUES($1,$2,$3,'ANDROID',$4)`, u, uuid.NewString(), uuid.NewString(), strings.Repeat("a", 64))
				}
			case "same public key":
				for _, u := range []int64{f.inviter, f.invitee} {
					f.exec(t, `INSERT INTO devices(user_id,device_uid,pubkey,platform) VALUES($1,$2,'same-public-key','ANDROID')`, u, uuid.NewString())
				}
			case "same prefix":
				f.exec(t, `UPDATE users SET register_ip_prefix_hash=$1 WHERE id IN($2,$3)`, strings.Repeat("b", 64), f.inviter, f.invitee)
			case "public payer":
				f.exec(t, `INSERT INTO shared_pay_addresses(address) VALUES('fixture-payer')`)
			case "mock payment":
				f.exec(t, `UPDATE chain_transfers SET network='MOCK' WHERE order_id=$1`, order)
			}
			if err := f.svc.OnOrderCompleted(f.ctx, f.event(order)); err != nil {
				t.Fatal(err)
			}
			want := "REVIEW"
			if signal == "same fingerprint" || signal == "same public key" {
				want = "CANCELLED"
			}
			if n, status, _ := f.state(t); n != 1 || status != want {
				t.Fatal(signal, n, status)
			}
			if err := f.svc.ReleaseDue(f.ctx, f.now.AddDate(1, 0, 0)); err != nil {
				t.Fatal(err)
			}
			f.assertNoGrant(t)
		})
	}
}

func TestRewardDaysTrafficAndRuleSnapshotsIntegration(t *testing.T) {
	f := newRewardFixture(t)
	oldExpire := f.now.Add(10 * 24 * time.Hour)
	f.exec(t, `UPDATE subscriptions SET tier=2,device_limit=3,expire_at=$2,period_quota_bytes=1000000,period_used_bytes=123 WHERE user_id=$1`, f.inviter, oldExpire)
	var planID int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO plans(code,name,plan_type,tier,pack_bytes,validity_days,device_limit,price_micro,status) VALUES('reward-traffic','test','TRAFFIC',1,1024,45,2,1000000,'ON_SALE') RETURNING id`).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO system_configs(key,value) VALUES('invite.reward_traffic_plan_code','"reward-traffic"')`)
	dayRule := f.rule(t, "INVITEE_FIRST_PAID", "DAYS", 7, 0, nil, nil)
	f.rule(t, "INVITEE_FIRST_PAID", "TRAFFIC", 4096, 0, nil, nil)
	order := f.order(t, f.now.Add(-time.Hour))
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(order)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE invite_reward_rules SET reward_value=999,status='DISABLED' WHERE id=$1`, dayRule)
	f.exec(t, `UPDATE plans SET validity_days=1,tier=9 WHERE id=$1`, planID)
	runRewardConcurrent(t, 6, func() error { return f.svc.ReleaseDue(f.ctx, f.now) })
	var expires time.Time
	var used int64
	var tier, devices int
	if err := f.pool.QueryRow(f.ctx, `SELECT expire_at,period_used_bytes,tier,device_limit FROM subscriptions WHERE user_id=$1`, f.inviter).Scan(&expires, &used, &tier, &devices); err != nil || !expires.Equal(oldExpire.Add(7*24*time.Hour)) || used != 123 || tier != 2 || devices != 3 {
		t.Fatal("days reward changed base plan", expires, used, tier, devices, err)
	}
	var bytes int64
	if err := f.pool.QueryRow(f.ctx, `SELECT total_bytes,tier,device_limit,expire_at FROM traffic_packs WHERE user_id=$1`, f.inviter).Scan(&bytes, &tier, &devices, &expires); err != nil || bytes != 4096 || tier != 1 || devices != 2 || !expires.Equal(f.now.Add(45*24*time.Hour)) {
		t.Fatal("traffic reward ignored snapshot", bytes, tier, devices, expires, err)
	}
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM subscription_ledger WHERE change_type='INVITE_REWARD'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate or absent entitlement entries", count, err)
	}
}

func TestRewardEachPaymentCapPercentageAndUncompletedEventIntegration(t *testing.T) {
	f := newRewardFixture(t)
	cap, bp := 1, 500
	f.rule(t, "INVITEE_PAID", "BALANCE", 0, 0, &cap, &bp)
	first := f.order(t, f.now.Add(-2*time.Hour))
	second := f.order(t, f.now.Add(-time.Hour))
	f.exec(t, `UPDATE orders SET status='PAID' WHERE id=$1`, first)
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(first)); err == nil {
		t.Fatal("uncompleted order event acknowledged")
	}
	if n, _, _ := f.state(t); n != 0 {
		t.Fatal("uncompleted order earned a reward")
	}
	f.exec(t, `UPDATE orders SET status='COMPLETED' WHERE id=$1`, first)
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(first)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.OnOrderCompleted(f.ctx, f.event(second)); err != nil {
		t.Fatal(err)
	}
	var pending, review int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FILTER(WHERE status='PENDING'),count(*) FILTER(WHERE status='REVIEW') FROM invite_rewards WHERE reward_value=500000`).Scan(&pending, &review); err != nil || pending != 1 || review != 1 {
		t.Fatal("percentage/cap failed", pending, review, err)
	}
	if err := f.svc.ReleaseDue(f.ctx, f.now); err != nil {
		t.Fatal(err)
	}
	var amount int64
	if err := f.pool.QueryRow(f.ctx, `SELECT available_micro FROM user_balances WHERE user_id=$1`, f.inviter).Scan(&amount); err != nil || amount != 500000 {
		t.Fatal("cap allowed extra credit", amount, err)
	}
}

func TestBalancePrimitiveOverflowReplayAndConflictingIdentityIntegration(t *testing.T) {
	f := newRewardFixture(t)
	e := balance.Entry{UserID: f.inviter, EntryType: "INVITE_REWARD", Amount: 42, SourceType: "INVITE_REWARD", SourceID: 10, IdemKey: "reward-test-once", Operator: "SYSTEM"}
	apply := func(entry balance.Entry) error {
		return rewardTransaction(f.ctx, f.pool, func(tx pgx.Tx) error { _, err := balance.Apply(f.ctx, tx, entry); return err })
	}
	runRewardConcurrent(t, 8, func() error { return apply(e) })
	var amount int64
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT available_micro,(SELECT count(*) FROM balance_ledger WHERE idempotency_key=$2) FROM user_balances WHERE user_id=$1`, f.inviter, e.IdemKey).Scan(&amount, &count); err != nil || amount != 42 || count != 1 {
		t.Fatal(amount, count, err)
	}
	for _, field := range []string{"amount", "user", "source", "type"} {
		other := e
		switch field {
		case "amount":
			other.Amount++
		case "user":
			other.UserID = f.invitee
		case "source":
			other.SourceID++
		case "type":
			other.SourceType = "ORDER"
		}
		if err := apply(other); !errors.Is(err, balance.ErrIdempotencyConflict) {
			t.Fatal("idempotency identity not checked", field, err)
		}
	}
	f.exec(t, `UPDATE user_balances SET available_micro=$2 WHERE user_id=$1`, f.inviter, int64(math.MaxInt64))
	e.IdemKey = "overflow"
	e.Amount = 1
	if err := apply(e); !errors.Is(err, balance.ErrOverflow) {
		t.Fatal("positive overflow accepted", err)
	}
	e.IdemKey = "underflow"
	e.Amount = money.Micro(math.MinInt64)
	if err := apply(e); !errors.Is(err, balance.ErrInsufficient) {
		t.Fatal("negative underflow accepted", err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT available_micro FROM user_balances WHERE user_id=$1`, f.inviter).Scan(&amount); err != nil || amount != math.MaxInt64 {
		t.Fatal("failed amount changed balance", amount, err)
	}
}
