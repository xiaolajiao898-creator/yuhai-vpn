package account

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func expectAuthError(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("expected auth code %d, got %v", code, err)
	}
}

func TestRefreshReplayRevokesDescendantsIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.Refresh(ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Refresh(ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	expectAuthError(t, err, httpx.ErrRefreshReplay)
	// Checking only the old token's error misses an accidental rollback of the
	// revocation. Its previously issued descendant must now be unusable as well.
	_, err = svc.Refresh(ctx, u.ID, deviceID, child.RefreshToken)
	expectAuthError(t, err, httpx.ErrRefreshReplay)
	var active int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE family_id=(SELECT family_id FROM auth_sessions WHERE refresh_hash=$1) AND revoked_at IS NULL`, auth.HashRefreshToken(registered.Tokens.RefreshToken)).Scan(&active); err != nil || active != 0 {
		t.Fatalf("replayed family retained active tokens: %d, %v", active, err)
	}
}

func TestConcurrentRefreshReplayCannotEscapeRevocationIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.Refresh(ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	var replayErr, rotateErr error
	var grandchild Tokens
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, replayErr = svc.Refresh(ctx, u.ID, deviceID, registered.Tokens.RefreshToken)
	}()
	go func() {
		defer wg.Done()
		<-start
		grandchild, rotateErr = svc.Refresh(ctx, u.ID, deviceID, child.RefreshToken)
	}()
	close(start)
	wg.Wait()
	expectAuthError(t, replayErr, httpx.ErrRefreshReplay)
	if rotateErr == nil {
		_, err = svc.Refresh(ctx, u.ID, deviceID, grandchild.RefreshToken)
		expectAuthError(t, err, httpx.ErrRefreshReplay)
	} else {
		expectAuthError(t, rotateErr, httpx.ErrRefreshReplay)
	}
	var active int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE family_id=(SELECT family_id FROM auth_sessions WHERE refresh_hash=$1) AND revoked_at IS NULL`, auth.HashRefreshToken(registered.Tokens.RefreshToken)).Scan(&active); err != nil || active != 0 {
		t.Fatalf("concurrent refresh escaped family revocation: %d, %v", active, err)
	}
}

func TestRestoreRechecksAccountUnderLockIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Repo.Pool.Exec(ctx, `UPDATE users SET secret_version=secret_version+1 WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	device.DeviceUID = uuid.NewString()
	_, err = svc.attachDevice(ctx, u, device, svc.Clock.Now(), "", deviceID)
	expectAuthError(t, err, httpx.ErrSecretRotated)
	if _, err = svc.Repo.Pool.Exec(ctx, `UPDATE users SET status='BANNED' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	u.SecretVersion++
	_, err = svc.attachDevice(ctx, u, device, svc.Clock.Now(), "", deviceID)
	expectAuthError(t, err, httpx.ErrAccountDisabled)
	var status string
	if err = svc.Repo.Pool.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1`, deviceID).Scan(&status); err != nil || status != "ACTIVE" {
		t.Fatalf("rejected replacement revoked original device: %s, %v", status, err)
	}
}

func TestRefreshWorksWithOneConnectionIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	cfg := svc.Repo.Pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	svc.Repo = &Repo{Pool: single}
	deviceID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := svc.Refresh(limited, u.ID, deviceID, registered.Tokens.RefreshToken)
	if err != nil || result.RefreshToken == "" {
		t.Fatalf("single-connection refresh failed: %v", err)
	}
}
