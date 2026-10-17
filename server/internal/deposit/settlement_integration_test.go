package deposit

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/order"
)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// These tests only use the explicitly selected, isolated test database. They
// insert MOCK transfers and never read DATABASE_URL or contact a chain endpoint.
func settlementDB(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func settlementUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
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
	lookup := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	if err := pool.QueryRow(ctx, `INSERT INTO users (account_no, secret_lookup, invite_code)
		VALUES ($1, $2, $3) RETURNING id`, account, lookup, invite).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func settlementAddress(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO receive_addresses (network, address, signature, sig_key_id, valid_until)
		VALUES ('MOCK', $1, 'integration-only', 'integration', now() + interval '1 day') RETURNING id`,
		"MOCK_TEST_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func settlementOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, addressID int64, status string, created, expires time.Time) string {
	t.Helper()
	no, err := ids.NewOrderNo(created)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO orders
		(order_no, user_id, order_type, base_amount_micro, pay_amount_micro, pay_method, receive_address_id, status, client_idem_key, created_at, expires_at)
		VALUES ($1, $2, 'TOPUP', 10000000, 10000100, 'USDT_TRC20', $3, $4, $5, $6, $7)`,
		no, userID, addressID, status, uuid.NewString(), created, expires)
	if err != nil {
		t.Fatal(err)
	}
	return no
}

func settlementTransfer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, addressID int64, blockTime time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO chain_transfers
		(network, tx_hash, contract_address, from_address, to_address, receive_address_id, amount_micro, block_number, block_time, confirm_status, data_source)
		SELECT 'MOCK', $1, 'MOCK', 'MOCK_PAYER', address, id, 10000100, 0, $2, 'SOLIDIFIED', 'MOCK'
		FROM receive_addresses WHERE id = $3 RETURNING id`, "mock-"+uuid.NewString(), blockTime, addressID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSettlementWindowIndependentOfExpiryWorkerIntegration(t *testing.T) {
	pool, ctx := settlementDB(t)
	created := time.Now().UTC().Truncate(time.Microsecond).Add(-4 * time.Hour)
	expires := created.Add(order.OrderTTL)
	end := expires.Add(order.DefaultCooldown)
	for _, status := range []string{"PENDING", "CONFIRMING", "EXPIRED", "CANCELLED"} {
		for _, tc := range []struct {
			name   string
			at     time.Time
			reason string
			isLate bool
		}{
			{"before_creation", created.Add(-time.Microsecond), "BEFORE_ORDER_CREATED", false},
			{"at_creation", created, "", false},
			{"after_expiry", expires.Add(time.Second), "", true},
			{"before_cooldown_end", end.Add(-time.Microsecond), "", true},
			{"at_cooldown_end", end, "LATE_BEYOND_COOLDOWN", false},
			{"after_cooldown_end", end.Add(time.Minute), "LATE_BEYOND_COOLDOWN", false},
		} {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				userID := settlementUser(t, ctx, pool)
				addressID := settlementAddress(t, ctx, pool)
				no := settlementOrder(t, ctx, pool, userID, addressID, status, created, expires)
				transferID := settlementTransfer(t, ctx, pool, addressID, tc.at)
				svc := Service{Pool: pool, Clock: fixedClock{end.Add(time.Hour)}}
				result, err := svc.Settle(ctx, transferID)
				if err != nil {
					t.Fatal(err)
				}
				var rows int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1`, userID).Scan(&rows); err != nil {
					t.Fatal(err)
				}
				if tc.reason != "" {
					if result.Outcome != "EXCEPTION" || result.Reason != tc.reason || rows != 0 {
						t.Fatalf("outside matching window: result=%+v ledger_rows=%d", result, rows)
					}
					return
				}
				if result.Outcome != "MATCHED" || result.OrderNo != no || rows != 1 {
					t.Fatalf("inside matching window: result=%+v ledger_rows=%d", result, rows)
				}
				var late bool
				if err := pool.QueryRow(ctx, `SELECT is_late_payment FROM orders WHERE order_no=$1`, no).Scan(&late); err != nil {
					t.Fatal(err)
				}
				wantLate := tc.isLate || status == "EXPIRED" || status == "CANCELLED"
				if late != wantLate {
					t.Fatalf("late flag=%v, want %v", late, wantLate)
				}
			})
		}
	}
}

func TestSettlementReusedAmountDoesNotCreditNewOrderIntegration(t *testing.T) {
	pool, ctx := settlementDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	created := now.Add(-4 * time.Hour)
	expires := created.Add(order.OrderTTL)
	oldEnd := expires.Add(order.DefaultCooldown)
	addressID := settlementAddress(t, ctx, pool)
	oldUser := settlementUser(t, ctx, pool)
	newUser := settlementUser(t, ctx, pool)
	oldNo := settlementOrder(t, ctx, pool, oldUser, addressID, "EXPIRED", created, expires)
	newCreated := oldEnd.Add(time.Second)
	newNo := settlementOrder(t, ctx, pool, newUser, addressID, "PENDING", newCreated, newCreated.Add(order.OrderTTL))
	// The old payment is observed late, after the same amount was allocated to a
	// new order. The previous backwards two-minute slack matched the new user.
	transferID := settlementTransfer(t, ctx, pool, addressID, oldEnd.Add(-time.Minute))
	svc := Service{Pool: pool, Clock: fixedClock{now}}
	const concurrent = 6
	var wg sync.WaitGroup
	results := make([]Result, concurrent)
	errs := make([]error, concurrent)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Settle(ctx, transferID)
		}(i)
	}
	wg.Wait()
	matched := 0
	for i, result := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if result.Outcome == "MATCHED" {
			matched++
			if result.OrderNo != oldNo {
				t.Fatalf("credited reused amount to wrong order: %+v", result)
			}
		} else if result.Outcome != "SKIPPED" {
			t.Fatalf("unexpected duplicate result: %+v", result)
		}
	}
	if matched != 1 {
		t.Fatalf("matched %d times, want exactly once", matched)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1`, newUser).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new account credited: count=%d error=%v", count, err)
	}
	var newStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE order_no=$1`, newNo).Scan(&newStatus); err != nil || newStatus != "PENDING" {
		t.Fatalf("new order changed: status=%s error=%v", newStatus, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1`, oldUser).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old payment not credited once: count=%d error=%v", count, err)
	}
}
