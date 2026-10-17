package account

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func TestRegistrationUsesBindingRiskChecksAndRetainsValidSessionIntegration(t *testing.T) {
	svc, ctx, u, template, _ := newAccountIntegrationFixture(t)
	var inviterCode string
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT invite_code FROM users WHERE id=$1`, u.ID).Scan(&inviterCode); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"invalid-code", "same-device", "blacklisted-inviter", "accepted"} {
		t.Run(scenario, func(t *testing.T) {
			device := template
			device.DeviceUID = uuid.NewString()
			if scenario != "same-device" {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				device.DevicePubKey = base64.StdEncoding.EncodeToString(der)
			}
			code := inviterCode
			if scenario == "invalid-code" {
				code = "000000"
			}
			if scenario == "blacklisted-inviter" {
				if _, err := svc.Repo.Pool.Exec(ctx, `INSERT INTO blacklist(target_type,target_value,effects,reason,created_by) VALUES('USER',$1,'{INVITE_REWARD}','integration',0)`, strconv.FormatInt(u.ID, 10)); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "accepted" {
				if _, err := svc.Repo.Pool.Exec(ctx, `UPDATE blacklist SET status='REMOVED' WHERE target_type='USER' AND target_value=$1`, strconv.FormatInt(u.ID, 10)); err != nil {
					t.Fatal(err)
				}
			}
			in := RegisterInput{DeviceInfo: device, Invite: &InviteHint{Code: code, Method: "APK_CHANNEL"}}
			raw, _ := json.Marshal(in)
			result, status, err := svc.Register(ctx, in, uuid.NewString(), raw, "", "")
			if err != nil || status != 201 || result.Tokens.AccessToken == "" || result.Tokens.RefreshToken == "" {
				t.Fatalf("optional invitation aborted registration: %d %v", status, err)
			}
			if result.InviteBound != (scenario == "accepted") {
				t.Fatalf("wrong binding result in %s: %v", scenario, result.InviteBound)
			}
			deviceID, _ := strconv.ParseInt(result.DeviceID, 10, 64)
			principal, err := svc.Repo.LookupDevice(ctx, deviceID)
			if err != nil || !principal.DeviceActive {
				t.Fatal("registration device missing after optional binding rejection", err)
			}
			var relations, sessions int
			if err := svc.Repo.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invite_relations WHERE invitee_user_id=$1),(SELECT count(*) FROM auth_sessions WHERE device_id=$2 AND revoked_at IS NULL)`, principal.UserID, deviceID).Scan(&relations, &sessions); err != nil {
				t.Fatal(err)
			}
			if sessions != 1 || (relations == 1) != result.InviteBound {
				t.Fatalf("registration not committed consistently: relations=%d sessions=%d", relations, sessions)
			}
		})
	}
}
