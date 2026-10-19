package order

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/money"
)

func tailDB(t *testing.T) (*pgxpool.Pool, context.Context) {
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

func tailUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users (account_no, secret_lookup, invite_code) VALUES ($1,$2,$3) RETURNING id`,
		account, lookup, invite).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func tailAddress(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO receive_addresses (network, address, signature, sig_key_id, valid_until, status)
		VALUES ('MOCK', $1, 'tail-test', 'tail-test', now() + interval '1 day', 'ACTIVE') RETURNING id`,
		"MOCK_TAIL_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTailIsOneCentAndQueueFillsIntegration(t *testing.T) {
	pool, ctx := tailDB(t)
	svc := &Service{Pool: pool, Clock: clock.Real{}}
	userID := tailUser(t, ctx, pool)
	addrID := tailAddress(t, ctx, pool)

	first, err := svc.Create(ctx, userID, CreateInput{OrderType: "TOPUP", Amount: "6", PayMethod: "USDT_TRC20"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	pay, err := money.Parse(first.PayAmount)
	if err != nil {
		t.Fatal(err)
	}
	base := money.Micro(6_000_000)
	tail := pay - base
	if first.ReceiveAddress == "" || tail < money.TailStep || tail > money.TailSlots*money.TailStep || tail%money.TailStep != 0 {
		t.Fatalf("pay=%s tail=%d address=%s", first.PayAmount, tail, first.ReceiveAddress)
	}
	second, err := svc.Create(ctx, userID, CreateInput{OrderType: "TOPUP", Amount: "6", PayMethod: "USDT_TRC20"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	secondPay, err := money.Parse(second.PayAmount)
	if err != nil {
		t.Fatal(err)
	}
	secondTail := secondPay - base
	if secondTail < money.TailStep || secondTail > money.TailSlots*money.TailStep || secondTail%money.TailStep != 0 || second.PayAmount == first.PayAmount && second.ReceiveAddress == first.ReceiveAddress {
		t.Fatalf("second pay=%s address=%s", second.PayAmount, second.ReceiveAddress)
	}

	for slot := int64(1); slot <= money.TailSlots; slot++ {
		amt := int64(base) + slot*int64(money.TailStep)
		if _, err := pool.Exec(ctx, `INSERT INTO amount_locks (receive_address_id, amount_micro, order_no, locked_until)
			VALUES ($1, $2, $3, now() + interval '3 hours')
			ON CONFLICT (receive_address_id, amount_micro) DO UPDATE SET locked_until = EXCLUDED.locked_until`,
			addrID, amt, "LOCK"+uuid.NewString()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	err = db.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, _, err := svc.allocateTail(ctx, tx, []receiveAddress{{ID: addrID}}, base, "QUEUEFULL", time.Now().Add(time.Minute))
		return err
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.ErrPayQueueFull {
		t.Fatalf("queue: %v", err)
	}
}
