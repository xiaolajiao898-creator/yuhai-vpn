package invite

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
)

type bindingUser struct {
	id            int64
	code, account string
}

func bindingDatabase(t *testing.T) (context.Context, *pgxpool.Pool, time.Time) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "invite-bind-test-" + uuid.NewString()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool, time.Now().UTC()
}

func insertBindingUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, created time.Time) bindingUser {
	t.Helper()
	account, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	code, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	u := bindingUser{account: account, code: code}
	err = pool.QueryRow(ctx, `INSERT INTO users(account_no,secret_lookup,invite_code,created_at) VALUES($1,$2,$3,$4) RETURNING id`, account, uuid.NewString(), code, created).Scan(&u.id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func relationCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invite_relations WHERE invitee_user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func insertBindingOrder(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, u int64, status string, paid *time.Time) int64 {
	t.Helper()
	var address, id int64
	err := q.QueryRow(ctx, `INSERT INTO receive_addresses(network,address,signature,sig_key_id,valid_until) VALUES('MOCK',$1,'test','test',now()+interval '1 day') RETURNING id`, "bind-test-"+uuid.NewString()).Scan(&address)
	if err != nil {
		t.Fatal(err)
	}
	err = q.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,order_type,base_amount_micro,pay_amount_micro,pay_method,receive_address_id,status,client_idem_key,expires_at,paid_at) VALUES($1,$2,'TOPUP',10000000,10000000,'USDT_TRC20',$3,$4,$5,now()+interval '1 hour',$6) RETURNING id`, uuid.NewString()[:24], u, address, status, uuid.NewString(), paid).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBindingSuccessIdempotencyAndPrivacyIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
	binder := &Binder{Pool: pool, Clock: fixedClock{now}}
	idem := uuid.NewString()
	result, err := binder.Bind(ctx, b.id, BindInput{" " + strings.ToLower(a.code) + " ", "MANUAL"}, idem)
	if err != nil || !result.Bound {
		t.Fatal(result, err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), a.account) || strings.Contains(string(encoded), a.code) {
		t.Fatal("binding exposed inviter identity")
	}
	binder.Clock = fixedClock{now.Add(100 * time.Hour)}
	repeated, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, idem)
	if err != nil || repeated != result || relationCount(t, ctx, pool, b.id) != 1 {
		t.Fatal(repeated, err)
	}
	_, err = binder.Bind(ctx, b.id, BindInput{a.code, "CLIPBOARD"}, idem)
	requireBindError(t, err, httpx.ErrConflict)
	_, err = binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
	requireBindError(t, err, httpx.ErrInviteBound)
	info, err := (&Service{Pool: pool, Clock: fixedClock{now}, BindingEnabled: true}).Summary(ctx, b.id)
	if err != nil || info.CanBind || info.BindStatus != "BOUND" {
		t.Fatal(info, err)
	}
}

func TestBindingWindowPaidAndSelfChecksIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	a := insertBindingUser(t, ctx, pool, now)
	binder := &Binder{Pool: pool, Clock: fixedClock{now}}
	_, err := binder.Bind(ctx, a.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
	requireBindError(t, err, httpx.ErrSelfInvite)
	for _, created := range []time.Time{now.Add(-72 * time.Hour), now.Add(time.Minute)} {
		b := insertBindingUser(t, ctx, pool, created)
		_, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
		requireBindError(t, err, httpx.ErrInviteWindow)
		if relationCount(t, ctx, pool, b.id) != 0 {
			t.Fatal("ineligible user was bound")
		}
	}
	for _, status := range []string{"PAID", "COMPLETED", "CANCELLED"} {
		b := insertBindingUser(t, ctx, pool, now)
		insertBindingOrder(t, ctx, pool, b.id, status, &now)
		_, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
		requireBindError(t, err, httpx.ErrInviteWindow)
	}
}

func TestBindingBlacklistAndRiskFlagsIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	binder := &Binder{Pool: pool, Clock: fixedClock{now}}
	for _, kind := range []string{"USER", "INVITE_CODE"} {
		a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
		value := a.code
		if kind == "USER" {
			value = strconv.FormatInt(a.id, 10)
		}
		var blacklistID int64
		if err := pool.QueryRow(ctx, `INSERT INTO blacklist(target_type,target_value,effects,reason,created_by) VALUES($1,$2,'{INVITE_REWARD}','integration',0) RETURNING id`, kind, value).Scan(&blacklistID); err != nil {
			t.Fatal(err)
		}
		_, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
		requireBindError(t, err, httpx.ErrRiskBlocked)
		if relationCount(t, ctx, pool, b.id) != 0 {
			t.Fatal("blacklisted invitation persisted")
		}
		if _, err := pool.Exec(ctx, `UPDATE blacklist SET expires_at=$2 WHERE id=$1`, blacklistID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString()); err != nil {
			t.Fatal("expired blacklist remained active", err)
		}
	}
	a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
	if _, err := pool.Exec(ctx, `UPDATE users SET register_ip_prefix_hash=$3 WHERE id IN ($1,$2)`, a.id, b.id, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var flags []byte
	if err := pool.QueryRow(ctx, `SELECT risk_flags FROM invite_relations WHERE invitee_user_id=$1`, b.id).Scan(&flags); err != nil || !strings.Contains(string(flags), "REGISTER_IP_PROXIMITY") {
		t.Fatal(string(flags), err)
	}
	var rewards int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invite_rewards WHERE invitee_user_id=$1`, b.id).Scan(&rewards); err != nil || rewards != 0 {
		t.Fatal("binding granted an unsolicited reward", rewards, err)
	}
}

func TestBindingSameDeviceIsBlockedIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	for _, fingerprint := range []bool{false, true} {
		a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
		for _, u := range []bindingUser{a, b} {
			pubkey, fp := "shared-key", ""
			if fingerprint {
				pubkey = uuid.NewString()
				fp = strings.Repeat("a", 64)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO devices(user_id,device_uid,pubkey,platform,fingerprint_hash) VALUES($1,$2,$3,'ANDROID',NULLIF($4,''))`, u.id, uuid.NewString(), pubkey, fp); err != nil {
				t.Fatal(err)
			}
		}
		_, err := (&Binder{Pool: pool, Clock: fixedClock{now}}).Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
		requireBindError(t, err, httpx.ErrRiskBlocked)
	}
}

func TestConcurrentBindingCannotCreateMutualInvitesIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
	binder := &Binder{Pool: pool, Clock: fixedClock{now}}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, pair := range [][2]bindingUser{{a, b}, {b, a}} {
		wg.Add(1)
		go func(i int, pair [2]bindingUser) {
			defer wg.Done()
			<-start
			_, errs[i] = binder.Bind(ctx, pair[0].id, BindInput{pair[1].code, "MANUAL"}, uuid.NewString())
		}(i, pair)
	}
	close(start)
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			requireBindError(t, err, httpx.ErrRiskBlocked)
		}
	}
	if succeeded != 1 || relationCount(t, ctx, pool, a.id)+relationCount(t, ctx, pool, b.id) != 1 {
		t.Fatalf("mutual invite created: %v", errs)
	}
}

func TestConcurrentBindingSameKeyCommitsOnceIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
	binder := &Binder{Pool: pool, Clock: fixedClock{now}}
	idem := uuid.NewString()
	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = binder.Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, idem) }(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if relationCount(t, ctx, pool, b.id) != 1 {
		t.Fatal("concurrent request created duplicate binding")
	}
	var successes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_idempotency WHERE user_id=$1 AND idem_key=$2`, b.id, idem).Scan(&successes); err != nil || successes != 1 {
		t.Fatal(successes, err)
	}
}

func TestBindingWaitsForPaymentCommitBeforeCheckingEligibilityIntegration(t *testing.T) {
	ctx, pool, now := bindingDatabase(t)
	a, b := insertBindingUser(t, ctx, pool, now), insertBindingUser(t, ctx, pool, now)
	payment, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer payment.Rollback(context.Background())
	if _, err := payment.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "invite-payment:"+strconv.FormatInt(b.id, 10)); err != nil {
		t.Fatal(err)
	}
	insertBindingOrder(t, ctx, payment, b.id, "COMPLETED", &now)
	finished := make(chan error, 1)
	go func() {
		_, err := (&Binder{Pool: pool, Clock: fixedClock{now}}).Bind(ctx, b.id, BindInput{a.code, "MANUAL"}, uuid.NewString())
		finished <- err
	}()
	// Observe the binder waiting at the payment lock before committing. Without
	// this barrier a scheduler might start binding only after the commit, making
	// a missing payment guard pass the test by accident.
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case err := <-finished:
			t.Fatalf("binding did not wait for the pending payment: %v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("binding never reached the payment guard")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := payment.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireBindError(t, <-finished, httpx.ErrInviteWindow)
	if relationCount(t, ctx, pool, b.id) != 0 {
		t.Fatal("binding crossed the first payment commit")
	}
}
