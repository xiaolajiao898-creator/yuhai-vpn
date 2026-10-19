package account

import (
	"testing"

	"github.com/google/uuid"
)

func TestSignInReusesDeviceIdentityIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	again, err := svc.signInUser(ctx, u, device, svc.Clock.Now(), "")
	if err != nil || again.DeviceID != registered.DeviceID {
		t.Fatalf("resume active device: %+v %v", again, err)
	}
	count, err := svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 1 {
		t.Fatalf("active after resume: %d %v", count, err)
	}
	var sessions int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions WHERE device_id=$1 AND revoked_at IS NULL`, mustDeviceID(t, registered.DeviceID)).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("refresh sessions after resume: %d %v", sessions, err)
	}

	other := device
	other.DeviceUID = uuid.NewString()
	other.Model = "second"
	second, err := svc.signInUser(ctx, u, other, svc.Clock.Now(), "")
	if err != nil || second.DeviceID == registered.DeviceID {
		t.Fatalf("new phone: %+v %v", second, err)
	}
	count, err = svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 2 {
		t.Fatalf("active after second phone: %d %v", count, err)
	}

	if err := svc.Logout(ctx, u.ID, mustDeviceID(t, registered.DeviceID), ""); err != nil {
		t.Fatal(err)
	}
	count, err = svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 1 {
		t.Fatalf("active after logout: %d %v", count, err)
	}
	back, err := svc.signInUser(ctx, u, device, svc.Clock.Now(), "")
	if err != nil || back.DeviceID != registered.DeviceID {
		t.Fatalf("reactivate same phone: %+v %v", back, err)
	}
	count, err = svc.Repo.countActiveDevices(ctx, svc.Repo.Pool, u.ID)
	if err != nil || count != 2 {
		t.Fatalf("active after reactivate: %d %v", count, err)
	}
}

func mustDeviceID(t *testing.T, id string) int64 {
	t.Helper()
	var n int64
	for _, c := range id {
		if c < '0' || c > '9' {
			t.Fatalf("device id %q", id)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
