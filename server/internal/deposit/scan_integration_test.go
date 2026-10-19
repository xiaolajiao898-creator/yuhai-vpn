package deposit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/order"
	"github.com/yuhai-vpn/server/internal/tron"
)

type fakeChain struct {
	headNum int64
	headAt  time.Time
	rows    []WatchedTransfer
	err     error
	calls   int
}

func (f *fakeChain) SolidifiedHead(context.Context) (int64, time.Time, error) {
	if f.err != nil {
		return 0, time.Time{}, f.err
	}
	return f.headNum, f.headAt, nil
}

func (f *fakeChain) IncomingUSDT(context.Context, string, time.Time, string) ([]WatchedTransfer, string, error) {
	f.calls++
	if f.err != nil {
		return nil, "", f.err
	}
	return f.rows, "", nil
}

func scanAddress(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (int64, string) {
	t.Helper()
	addr := "T" + uuid.NewString()
	var id int64
	err := pool.QueryRow(ctx, `INSERT INTO receive_addresses (network, address, signature, sig_key_id, valid_until, status)
		VALUES ('TRON', $1, 'scan-test', 'scan-test', now() + interval '1 day', 'ACTIVE') RETURNING id`, addr).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id, addr
}

func scanOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, addressID, pay int64, created, expires time.Time) string {
	t.Helper()
	no, err := ids.NewOrderNo(created)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO orders
		(order_no, user_id, order_type, base_amount_micro, pay_amount_micro, pay_method, receive_address_id, status, client_idem_key, created_at, expires_at)
		VALUES ($1, $2, 'TOPUP', $3, $4, 'USDT_TRC20', $5, 'PENDING', $6, $7, $8)`,
		no, userID, pay-10_000, pay, addressID, uuid.NewString(), created, expires)
	if err != nil {
		t.Fatal(err)
	}
	return no
}

func TestScanUSDTMatchesSettlesAndIgnoresBadTransfersIntegration(t *testing.T) {
	pool, ctx := settlementDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	svc := &Service{Pool: pool, Clock: clock.Real{}}
	pay := int64(6_010_000)
	block := now.Add(-time.Minute)

	t.Run("match", func(t *testing.T) {
		userID := settlementUser(t, ctx, pool)
		addrID, addr := scanAddress(t, ctx, pool)
		no := scanOrder(t, ctx, pool, userID, addrID, pay, now.Add(-2*time.Minute), now.Add(20*time.Minute))
		src := &fakeChain{headNum: 1000, headAt: now, rows: []WatchedTransfer{{
			TxHash: uuid.NewString(), EventIndex: 0, Contract: tron.OfficialUSDT,
			From: "TPAYER", To: addr, AmountMicro: pay, BlockNumber: 10, BlockTime: block, Confirmed: true,
		}}}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertOrder(t, ctx, pool, no, "COMPLETED")
		assertTransfers(t, ctx, pool, addrID, 1)
		var deposits int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='DEPOSIT'`, userID).Scan(&deposits); err != nil || deposits != 1 {
			t.Fatalf("deposits=%d err=%v", deposits, err)
		}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertTransfers(t, ctx, pool, addrID, 1)
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='DEPOSIT'`, userID).Scan(&deposits); err != nil || deposits != 1 {
			t.Fatalf("duplicate deposits=%d err=%v", deposits, err)
		}
	})

	t.Run("wrong contract", func(t *testing.T) {
		userID := settlementUser(t, ctx, pool)
		addrID, addr := scanAddress(t, ctx, pool)
		scanOrder(t, ctx, pool, userID, addrID, pay, now.Add(-2*time.Minute), now.Add(20*time.Minute))
		src := &fakeChain{headNum: 1000, headAt: now, rows: []WatchedTransfer{{
			TxHash: uuid.NewString(), Contract: "TFakeToken", From: "TPAYER", To: addr,
			AmountMicro: pay, BlockNumber: 10, BlockTime: block, Confirmed: true,
		}}}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertTransfers(t, ctx, pool, addrID, 0)
	})

	t.Run("wrong amount", func(t *testing.T) {
		userID := settlementUser(t, ctx, pool)
		addrID, addr := scanAddress(t, ctx, pool)
		no := scanOrder(t, ctx, pool, userID, addrID, pay, now.Add(-2*time.Minute), now.Add(20*time.Minute))
		src := &fakeChain{headNum: 1000, headAt: now, rows: []WatchedTransfer{{
			TxHash: uuid.NewString(), Contract: tron.OfficialUSDT, From: "TPAYER", To: addr,
			AmountMicro: pay + 10_000, BlockNumber: 10, BlockTime: block, Confirmed: true,
		}}}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertOrder(t, ctx, pool, no, "PENDING")
		var reason string
		if err := pool.QueryRow(ctx, `SELECT e.reason FROM deposit_exceptions e
			JOIN chain_transfers c ON c.id = e.chain_transfer_id WHERE c.receive_address_id=$1`, addrID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != "AMOUNT_MISMATCH" {
			t.Fatal(reason)
		}
	})

	t.Run("expired beyond cooldown", func(t *testing.T) {
		userID := settlementUser(t, ctx, pool)
		addrID, addr := scanAddress(t, ctx, pool)
		created := now.Add(-6 * time.Hour)
		no := scanOrder(t, ctx, pool, userID, addrID, pay, created, created.Add(order.OrderTTL))
		src := &fakeChain{headNum: 1000, headAt: now, rows: []WatchedTransfer{{
			TxHash: uuid.NewString(), Contract: tron.OfficialUSDT, From: "TPAYER", To: addr,
			AmountMicro: pay, BlockNumber: 10, BlockTime: now.Add(-time.Minute), Confirmed: true,
		}}}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertOrder(t, ctx, pool, no, "PENDING")
		var reason string
		if err := pool.QueryRow(ctx, `SELECT reason FROM deposit_exceptions e
			JOIN chain_transfers c ON c.id = e.chain_transfer_id WHERE c.receive_address_id=$1`, addrID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != "LATE_BEYOND_COOLDOWN" {
			t.Fatal(reason)
		}
	})

	t.Run("unconfirmed then confirmed", func(t *testing.T) {
		userID := settlementUser(t, ctx, pool)
		addrID, addr := scanAddress(t, ctx, pool)
		no := scanOrder(t, ctx, pool, userID, addrID, pay, now.Add(-2*time.Minute), now.Add(20*time.Minute))
		tx := uuid.NewString()
		src := &fakeChain{headNum: 1000, headAt: now, rows: []WatchedTransfer{{
			TxHash: tx, Contract: tron.OfficialUSDT, From: "TPAYER", To: addr,
			AmountMicro: pay, BlockNumber: 10, BlockTime: block, Confirmed: false,
		}}}
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertTransfers(t, ctx, pool, addrID, 0)
		var cursors int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM chain_scan_cursors WHERE receive_address_id=$1`, addrID).Scan(&cursors); err != nil || cursors != 0 {
			t.Fatalf("cursor moved on unconfirmed: %d %v", cursors, err)
		}
		src.rows[0].Confirmed = true
		if err := svc.ScanUSDT(ctx, src, now); err != nil {
			t.Fatal(err)
		}
		assertOrder(t, ctx, pool, no, "COMPLETED")
		assertTransfers(t, ctx, pool, addrID, 1)
	})

	t.Run("rate limit does not advance cursor", func(t *testing.T) {
		addrID, _ := scanAddress(t, ctx, pool)
		src := &fakeChain{err: errors.New("HTTP 429")}
		if err := svc.ScanUSDT(ctx, src, now); err == nil {
			t.Fatal("expected rate limit")
		}
		var cursors int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM chain_scan_cursors WHERE receive_address_id=$1`, addrID).Scan(&cursors); err != nil || cursors != 0 {
			t.Fatalf("cursor=%d err=%v", cursors, err)
		}
	})
}

func assertOrder(t *testing.T, ctx context.Context, pool *pgxpool.Pool, no, status string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM orders WHERE order_no=$1`, no).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != status {
		t.Fatalf("order %s status %s, want %s", no, got, status)
	}
}

func assertTransfers(t *testing.T, ctx context.Context, pool *pgxpool.Pool, addrID int64, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM chain_transfers WHERE receive_address_id=$1`, addrID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("transfers=%d want %d", n, want)
	}
}
