package account

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func attachRemovalTarget(t *testing.T, svc *Service, ctx context.Context, u userRow, d DeviceInfo) (int64, RestoreResult) {
	t.Helper()
	d.DeviceUID = uuid.NewString()
	res, err := svc.attachDevice(ctx, u, d, svc.Clock.Now(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(res.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id, res
}

func assertDeviceRevocation(t *testing.T, svc *Service, ctx context.Context, deviceID int64, wantStatus string, wantLogs int) {
	t.Helper()
	var deviceStatus, credentialStatus string
	var revokedAt bool
	var activeSessions, logs int
	err := svc.Repo.Pool.QueryRow(ctx, `SELECT d.status, c.status, d.revoked_at IS NOT NULL,
		(SELECT count(*) FROM auth_sessions WHERE device_id=d.id AND revoked_at IS NULL),
		(SELECT count(*) FROM device_bind_logs WHERE device_id=d.id AND action='REVOKE_BY_USER')
		FROM devices d JOIN device_credentials c ON c.device_id=d.id WHERE d.id=$1`, deviceID).
		Scan(&deviceStatus, &credentialStatus, &revokedAt, &activeSessions, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if deviceStatus != wantStatus || credentialStatus != wantStatus || logs != wantLogs || revokedAt != (wantStatus == "REVOKED") {
		t.Fatalf("inconsistent revocation: device=%s credential=%s revokedAt=%v logs=%d", deviceStatus, credentialStatus, revokedAt, logs)
	}
	if (wantStatus == "REVOKED" && activeSessions != 0) || (wantStatus == "ACTIVE" && activeSessions == 0) {
		t.Fatalf("session revocation mismatch: status=%s activeSessions=%d", wantStatus, activeSessions)
	}
}

func TestRemoveDeviceRevokesAccessAndIsIdempotentIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	targetID, restored := attachRemovalTarget(t, svc, ctx, u, device)
	child, err := svc.Refresh(ctx, u.ID, targetID, restored.Tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{Svc: svc}
	r := gin.New()
	r.DELETE("/devices/:device_id", func(c *gin.Context) { httpx.SetPrincipal(c, u.ID, currentID) }, h.removeDevice)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/devices/"+restored.DeviceID, nil).WithContext(ctx)
		req.Header.Set("Idempotency-Key", uuid.NewString())
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected remove response: %d %s", w.Code, w.Body.String())
		}
	}
	assertDeviceRevocation(t, svc, ctx, targetID, "REVOKED", 1)
	assertDeviceRevocation(t, svc, ctx, currentID, "ACTIVE", 0)
	p, err := svc.Repo.LookupDevice(ctx, targetID)
	if err != nil || p.DeviceActive {
		t.Fatalf("revoked device still authenticates: %v %v", p.DeviceActive, err)
	}
	_, err = svc.Refresh(ctx, u.ID, targetID, child.RefreshToken)
	expectAuthError(t, err, httpx.ErrRefreshReplay)
}

func TestRemoveDeviceEnforcesOwnershipAndCurrentDeviceIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	_, _, otherUser, _, other := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	otherID, _ := strconv.ParseInt(other.DeviceID, 10, 64)
	if u.ID == otherUser.ID {
		t.Fatal("test users must be distinct")
	}
	foreignErr := svc.RemoveDevice(ctx, u.ID, currentID, otherID, "")
	missingErr := svc.RemoveDevice(ctx, u.ID, currentID, 9223372036854775807, "")
	expectAuthError(t, foreignErr, httpx.ErrNotFound)
	expectAuthError(t, missingErr, httpx.ErrNotFound)
	if foreignErr.Error() != missingErr.Error() {
		t.Fatal("foreign and nonexistent devices disclosed different errors")
	}
	expectAuthError(t, svc.RemoveDevice(ctx, u.ID, currentID, currentID, ""), httpx.ErrBadRequest)
	assertDeviceRevocation(t, svc, ctx, otherID, "ACTIVE", 0)
	assertDeviceRevocation(t, svc, ctx, currentID, "ACTIVE", 0)
}

func TestRemoveDeviceRollsBackAllRevocationOnAuditFailureIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	targetID, _ := attachRemovalTarget(t, svc, ctx, u, device)
	// The audit column rejects an oversized hash after the updates have run.
	// A failure must leave the device, credentials and sessions active together.
	if err := svc.RemoveDevice(ctx, u.ID, currentID, targetID, strings.Repeat("f", 80)); err == nil {
		t.Fatal("expected audit insert failure")
	}
	assertDeviceRevocation(t, svc, ctx, targetID, "ACTIVE", 0)
	if err := svc.RemoveDevice(ctx, u.ID, currentID, targetID, ""); err != nil {
		t.Fatal(err)
	}
	assertDeviceRevocation(t, svc, ctx, targetID, "REVOKED", 1)
}

func TestConcurrentRemoveAndRefreshCannotLeaveActiveSessionsIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	targetID, restored := attachRemovalTarget(t, svc, ctx, u, device)
	const attempts = 8
	var wg sync.WaitGroup
	wg.Add(attempts + 1)
	start := make(chan struct{})
	errCh := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			<-start
			errCh <- svc.RemoveDevice(ctx, u.ID, currentID, targetID, "")
		}()
	}
	var refreshed Tokens
	var refreshErr error
	go func() {
		defer wg.Done()
		<-start
		refreshed, refreshErr = svc.Refresh(ctx, u.ID, targetID, restored.Tokens.RefreshToken)
	}()
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if refreshErr == nil {
		_, err := svc.Refresh(ctx, u.ID, targetID, refreshed.RefreshToken)
		expectAuthError(t, err, httpx.ErrRefreshReplay)
	} else {
		var apiErr *httpx.Error
		if !errors.As(refreshErr, &apiErr) || (apiErr.Code != httpx.ErrRefreshReplay && apiErr.Code != httpx.ErrDeviceRevoked) {
			t.Fatal(refreshErr)
		}
	}
	assertDeviceRevocation(t, svc, ctx, targetID, "REVOKED", 1)
}

func TestConcurrentMutualDeviceRemovalOnlyOneSucceedsIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	firstID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	secondID, _ := attachRemovalTarget(t, svc, ctx, u, device)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	errs := make([]error, 2)
	for i, pair := range [][2]int64{{firstID, secondID}, {secondID, firstID}} {
		go func(i int, pair [2]int64) {
			defer wg.Done()
			<-start
			errs[i] = svc.RemoveDevice(ctx, u.ID, pair[0], pair[1], "")
		}(i, pair)
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else {
			expectAuthError(t, err, httpx.ErrDeviceRevoked)
		}
	}
	if successes != 1 {
		t.Fatalf("expected one successful removal, got %d", successes)
	}
	count, err := svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 1 {
		t.Fatalf("mutual removal left %d active devices: %v", count, err)
	}
}
