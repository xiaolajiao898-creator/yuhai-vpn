package invite

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/ids"
)

// Only connects to TEST_DATABASE_URL. All fixtures and service reads share one
// transaction which is rolled back, including on failure; no live money moves.
func TestInviteReadIsolationAndPaginationIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	newUser := func() int64 {
		t.Helper()
		accountNo, err := ids.NewAccountNo()
		if err != nil {
			t.Fatal(err)
		}
		code, err := ids.NewInviteCode()
		if err != nil {
			t.Fatal(err)
		}
		var user int64
		err = tx.QueryRow(ctx, `INSERT INTO users(account_no,secret_lookup,invite_code,created_at)
		 VALUES($1,$2,$3,$4) RETURNING id`, accountNo, uuid.NewString(), code, now).Scan(&user)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	a, b, c, d := newUser(), newUser(), newUser(), newUser()
	svc := &Service{Pool: tx, Clock: fixedClock{now}, BindingEnabled: true}
	info, err := svc.Summary(ctx, a)
	if err != nil || !info.CanBind || !info.BindingEnabled || info.BindStatus != "AVAILABLE" || info.InviteURL != nil || info.Stats != (Stats{}) {
		t.Fatal(info, err)
	}
	svc.Clock = fixedClock{now.Add(72 * time.Hour)}
	if info, err = svc.Summary(ctx, a); err != nil || info.CanBind {
		t.Fatal("binding remained eligible at deadline", info, err)
	}
	svc.Clock = fixedClock{now}
	for _, invitee := range []int64{b, c} {
		if _, err := tx.Exec(ctx, `INSERT INTO invite_relations(invitee_user_id,inviter_user_id,invite_code,bind_method)
		 VALUES($1,$2,'ABC234','MANUAL')`, invitee, a); err != nil {
			t.Fatal(err)
		}
	}
	if info, err = svc.Summary(ctx, b); err != nil || info.CanBind {
		t.Fatal("already bound account remained eligible", info, err)
	}
	var addressID int64
	err = tx.QueryRow(ctx, `INSERT INTO receive_addresses(network,address,signature,sig_key_id,valid_until)
	 VALUES('MOCK',$1,'integration','integration',$2) RETURNING id`, "invite-test-"+uuid.NewString(), now.Add(time.Hour)).Scan(&addressID)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []int64{b, b, a} {
		if _, err := tx.Exec(ctx, `INSERT INTO orders(order_no,user_id,order_type,base_amount_micro,pay_amount_micro,
		 pay_method,receive_address_id,status,client_idem_key,expires_at,completed_at)
		 VALUES($1,$2,'TOPUP',10000000,10000000,'USDT_TRC20',$3,'COMPLETED',$4,$5,$6)`,
			uuid.NewString()[:24], user, addressID, uuid.NewString(), now.Add(time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}
	var rule int64
	if err := tx.QueryRow(ctx, `INSERT INTO invite_reward_rules(name,trigger_event,beneficiary,reward_type,reward_value)
	 VALUES('integration','INVITEE_FIRST_PAID','INVITER','DAYS',7) RETURNING id`).Scan(&rule); err != nil {
		t.Fatal(err)
	}
	addReward := func(inviter, invitee, beneficiary int64, kind, status string, value int64) string {
		t.Helper()
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO invite_rewards(rule_id,rule_snapshot,inviter_user_id,invitee_user_id,
		 beneficiary_user_id,trigger_ref,reward_type,reward_value,status,release_at,created_at,risk_flags,cancel_reason)
		 VALUES($1,'{}',$2,$3,$4,$5,$6,$7,$8,$9,$10,'["PRIVATE_RISK"]','private reason') RETURNING id`,
			rule, inviter, invitee, beneficiary, uuid.NewString(), kind, value, status, now.Add(72*time.Hour), now).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return strconv.FormatInt(id, 10)
	}
	first := addReward(a, b, a, "DAYS", "PENDING", 7)
	second := addReward(a, c, a, "TRAFFIC", "RELEASED", 1073741824)
	_ = addReward(a, c, c, "BALANCE", "RELEASED", 9999999) // Inviter cannot read someone else's benefit.
	third := addReward(d, a, a, "BALANCE", "REVIEW", 1000043)
	fourth := addReward(d, a, a, "BALANCE", "CANCELLED", 0)
	info, err = svc.Summary(ctx, a)
	if err != nil || info.CanBind || info.Stats != (Stats{Invited: 2, Paid: 1, RewardsPending: 2, RewardsReleased: 1}) {
		t.Fatal("incorrect counts or paid binding eligibility", info, err)
	}
	page, err := svc.Rewards(ctx, a, 2, 0)
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].ID != fourth || page.Items[1].ID != third {
		t.Fatal(page, err)
	}
	if page.Items[0].Status != "REJECTED" || page.Items[0].ReleaseAt != nil || page.Items[1].Status != "REVIEW" || page.Items[1].ReleaseAt != nil || page.Items[1].Role != "INVITEE" || *page.Items[1].Amount != "1.000043" {
		t.Fatal("incorrect public lifecycle or amount", page)
	}
	raw, err := json.Marshal(page)
	if err != nil || strings.Contains(string(raw), "PRIVATE_RISK") || strings.Contains(string(raw), "private reason") || strings.Contains(string(raw), "invitee_user_id") {
		t.Fatal("leaked internal reward metadata", string(raw), err)
	}
	_, before, err := rewardParams(url.Values{"cursor": {*page.NextCursor}}, a)
	if err != nil {
		t.Fatal(err)
	}
	_ = addReward(a, b, a, "DAYS", "PENDING", 1) // New rows cannot repeat/shift older pages.
	page, err = svc.Rewards(ctx, a, 2, before)
	if err != nil || len(page.Items) != 2 || page.NextCursor != nil || page.Items[0].ID != second || page.Items[1].ID != first || *page.Items[0].Bytes != 1073741824 || *page.Items[1].Days != 7 {
		t.Fatal(page, err)
	}
	page, err = svc.Rewards(ctx, b, 20, 0)
	if err != nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatal("invitee saw inviter rewards", page, err)
	}
	if _, err := svc.Summary(ctx, 0); err == nil {
		t.Fatal("summary accepted missing identity")
	}
	if _, err := svc.Rewards(ctx, 0, 20, 0); err == nil {
		t.Fatal("rewards accepted missing identity")
	}
}
