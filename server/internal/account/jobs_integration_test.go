package account

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/ids"
)

func TestPurgeJobsRunAsAppRoleIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	setup, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.Close)

	appCfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	appCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE yuhai_app`)
		return err
	}
	app, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	if err := app.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	accountNo, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	var userID int64
	if err := setup.QueryRow(ctx, `INSERT INTO users (account_no, secret_lookup, invite_code, register_ip_hash, register_ip_prefix_hash, created_at)
		VALUES ($1, $2, $3, $4, $4, now() - interval '40 days') RETURNING id`,
		accountNo, strings.Repeat("cd", 32), invite, hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var deviceID int64
	if err := setup.QueryRow(ctx, `INSERT INTO devices (user_id, device_uid, pubkey, platform) VALUES ($1, $2, 'purge', 'ANDROID') RETURNING id`,
		userID, uuid.New()).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO device_bind_logs (user_id, device_id, action, operator_type, ip_hash, idempotency_key, created_at)
		VALUES ($1, $2, 'REGISTER', 'USER', $3, $4, now() - interval '40 days')`,
		userID, deviceID, hash, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO auth_sessions (user_id, device_id, refresh_hash, family_id, secret_version, expires_at)
		VALUES ($1, $2, $3, $4, 1, now() - interval '100 days')`,
		userID, deviceID, strings.Repeat("e", 64), uuid.New()); err != nil {
		t.Fatal(err)
	}

	jobs := &Jobs{Repo: &Repo{Pool: app}}
	if err := jobs.PurgeIPHashes(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.PurgeExpiredSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var ipHash *string
	var sessions int
	if err := setup.QueryRow(ctx, `SELECT register_ip_hash FROM users WHERE id=$1`, userID).Scan(&ipHash); err != nil || ipHash != nil {
		t.Fatalf("register ip hash %v %v", ipHash, err)
	}
	if err := setup.QueryRow(ctx, `SELECT ip_hash FROM device_bind_logs WHERE user_id=$1`, userID).Scan(&ipHash); err != nil || ipHash != nil {
		t.Fatalf("bind ip hash %v %v", ipHash, err)
	}
	if err := setup.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id=$1`, userID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions=%d err=%v", sessions, err)
	}
}
