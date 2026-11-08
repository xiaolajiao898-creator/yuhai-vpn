package worldvpn

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/fieldenc"
)

func TestAccountRoundTripIntegration(t *testing.T) {
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	appCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ownerCfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var redeemCols string
	if err = owner.QueryRow(ctx, `SELECT coalesce(string_agg(column_name, ',' ORDER BY column_name), '') FROM information_schema.columns WHERE table_schema='public' AND table_name='redeem_codes'`).Scan(&redeemCols); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"duration_days", "batch_id", "card_type", "code_hash", "status"} {
		if !strings.Contains(redeemCols, col) {
			t.Fatalf("public.redeem_codes missing %s: %s", col, redeemCols)
		}
	}
	if strings.Contains(redeemCols, "period_months") {
		t.Fatal("public.redeem_codes gained period_months")
	}
	schema := "worldvpn_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, _ = owner.Exec(c, "DROP SCHEMA "+quoted+" CASCADE")
	})
	for _, table := range []string{"users", "worldvpn_accounts", "worldvpn_grants"} {
		q := "CREATE TABLE " + pgx.Identifier{schema, table}.Sanitize() + " (LIKE " + pgx.Identifier{"public", table}.Sanitize() + " INCLUDING ALL)"
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	role := pgx.Identifier{appCfg.ConnConfig.User}.Sanitize()
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + quoted + " TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA " + quoted + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	appCfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int64
	if err = pool.QueryRow(ctx, `INSERT INTO users(account_no, secret_lookup, invite_code) VALUES ('8302571644', $1, 'INVITECODE12') RETURNING id`, strings.Repeat("ab", 32)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := fieldenc.New("test-key", []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	store := &PG{Pool: pool, Cipher: cipher}
	exp := time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC)
	if err = store.SaveAccount(ctx, account{
		UserID: userID, Username: "yh8302571644", Password: "local-vpn-password",
		Status: "ACTIVE", ExpireAt: exp, Source: "ORDER", Sandbox: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Account(ctx, userID)
	if err != nil || !ok || got.Password != "local-vpn-password" || got.Username != "yh8302571644" || !got.Sandbox {
		t.Fatalf("round trip: ok=%v err=%v user=%s sandbox=%v", ok, err, got.Username, got.Sandbox)
	}
	access, err := store.LineAccess(ctx, userID, exp.Add(-time.Hour), true)
	if err != nil || !access.Available {
		t.Fatalf("access: %+v %v", access, err)
	}
	var stored string
	if err = pool.QueryRow(ctx, `SELECT encode(password_enc, 'escape') FROM worldvpn_accounts WHERE user_id=$1`, userID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "local-vpn-password") {
		t.Fatal("vpn password was stored in clear text")
	}
}
