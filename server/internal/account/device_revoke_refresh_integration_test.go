package account

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/auth"
)

// Hold the same transaction that Refresh uses after rotation and insertion of
// its child token, but before COMMIT. Starting revocation now reproduces the
// snapshot race deterministically: without the shared advisory lock, the
// revoker's UPDATE waits on the old session and misses the uncommitted child.
func holdInFlightRefresh(t *testing.T, svc *Service, ctx context.Context, userID, deviceID int64, refreshToken string) (pgx.Tx, int32) {
	t.Helper()
	tx, err := svc.Repo.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err := svc.Repo.lockDeviceSessions(ctx, tx, deviceID); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Repo.sessionByHashForUpdate(ctx, tx, auth.HashRefreshToken(refreshToken))
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Repo.lookupDevice(ctx, tx, deviceID)
	if err != nil || !p.DeviceActive {
		t.Fatalf("refresh fixture device inactive: %v", err)
	}
	now := svc.Clock.Now()
	if err := svc.Repo.markRotated(ctx, tx, sess.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.issueTokensInFamily(ctx, tx, userID, deviceID, p.PubKeySPKI, p.SecretVersion, sess.FamilyID, now); err != nil {
		t.Fatal(err)
	}
	var pid int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func waitForBlockedRevocation(t *testing.T, svc *Service, ctx context.Context, refreshPID int32) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := svc.Repo.Pool.QueryRow(waitCtx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, refreshPID).Scan(&blocked); err != nil {
			t.Fatalf("revocation did not reach the in-flight refresh: %v", err)
		}
		if blocked {
			return
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("revocation did not wait for the in-flight refresh")
		case <-ticker.C:
		}
	}
}

func TestLogoutRevokesInFlightRefreshDescendantIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	refreshTx, refreshPID := holdInFlightRefresh(t, svc, ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	result := make(chan error, 1)
	go func() { result <- svc.Logout(ctx, u.ID, deviceID, "") }()
	waitForBlockedRevocation(t, svc, ctx, refreshPID)
	if err := refreshTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	assertDeviceRevocation(t, svc, ctx, deviceID, "REVOKED", 1)
}

func TestReplacementRevokesInFlightRefreshDescendantIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	refreshTx, refreshPID := holdInFlightRefresh(t, svc, ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	device.DeviceUID = uuid.NewString()
	type replacementResult struct {
		device RestoreResult
		err    error
	}
	result := make(chan replacementResult, 1)
	go func() {
		replacement, err := svc.attachDevice(ctx, u, device, svc.Clock.Now(), "", deviceID)
		result <- replacementResult{device: replacement, err: err}
	}()
	waitForBlockedRevocation(t, svc, ctx, refreshPID)
	if err := refreshTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	replacement := <-result
	if replacement.err != nil {
		t.Fatal(replacement.err)
	}
	replacementID, err := strconv.ParseInt(replacement.device.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	assertDeviceRevocation(t, svc, ctx, deviceID, "REVOKED", 1)
	assertDeviceRevocation(t, svc, ctx, replacementID, "ACTIVE", 0)
	count, err := svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 1 {
		t.Fatalf("replacement left %d active devices: %v", count, err)
	}
}
