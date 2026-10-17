package account

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func newAccountIntegrationFixture(t *testing.T) (*Service, context.Context, userRow, DeviceInfo, RegisterResult) {
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
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := fieldenc.New("integration", key)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: &Repo{Pool: pool}, Signer: auth.NewSigner("integration", key), Enc: enc, Clock: clock.Real{}, Pepper: key}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	device := DeviceInfo{DeviceUID: uuid.NewString(), DevicePubKey: base64.StdEncoding.EncodeToString(der), Platform: "ANDROID", Model: "integration", OSVersion: "16", AppVersion: "1.0.0+100"}
	in := RegisterInput{DeviceInfo: device}
	raw, _ := json.Marshal(in)
	registered, _, err := svc.Register(ctx, in, uuid.NewString(), raw, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT user_id FROM devices WHERE device_uid=$1`, device.DeviceUID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	u, err := svc.Repo.userByID(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, ctx, u, device, registered
}

func TestConcurrentDeviceRestoreRespectsLimitIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	pool, userID := svc.Repo.Pool, u.ID
	// One device is active and the account limit is two. Start eight independent
	// restore transactions together; only one may consume the final slot.
	const attempts = 8
	results := make([]RestoreResult, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(attempts)
	done.Add(attempts)
	for i := range results {
		go func(i int) {
			defer done.Done()
			d := device
			d.DeviceUID = uuid.NewString()
			ready.Done()
			<-start
			results[i], errs[i] = svc.attachDevice(ctx, u, d, svc.Clock.Now(), "", 0)
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			if results[i].DeviceID == "" {
				t.Fatal("successful restore missing device")
			}
			continue
		}
		var apiErr *httpx.Error
		if !errors.As(err, &apiErr) || apiErr.Code != httpx.ErrDeviceLimit {
			t.Fatal(err)
		}
	}
	count, err := svc.Repo.countActiveDevices(ctx, pool, userID)
	if err != nil || success != 1 || count != 2 {
		t.Fatalf("limit bypass: successes=%d active=%d error=%v", success, count, err)
	}
	// Replacement shares the same lock but still works at the limit: revoke and
	// issue the replacement in one transaction, without leaking an extra slot.
	removeID, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	replacement := device
	replacement.DeviceUID = uuid.NewString()
	if _, err := svc.attachDevice(ctx, u, replacement, svc.Clock.Now(), "", removeID); err != nil {
		t.Fatal(err)
	}
	count, err = svc.Repo.countActiveDevices(ctx, pool, userID)
	if err != nil || count != 2 {
		t.Fatalf("replacement changed active device count: %d, %v", count, err)
	}
	var activeSessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE device_id=$1 AND revoked_at IS NULL`, removeID).Scan(&activeSessions); err != nil || activeSessions != 0 {
		t.Fatalf("removed device retained sessions: %d, %v", activeSessions, err)
	}
}
