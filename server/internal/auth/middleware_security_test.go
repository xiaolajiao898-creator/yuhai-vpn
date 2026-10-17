package auth

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type securityClock struct{ now time.Time }

func (c securityClock) Now() time.Time { return c.now }

type securityStore struct{ principal DevicePrincipal }

func (s securityStore) LookupDevice(context.Context, int64) (DevicePrincipal, error) {
	return s.principal, nil
}

func TestS1RejectsTokenPrincipalIdentityMismatch(t *testing.T) {
	now := time.Unix(2000000000, 0)
	signer := NewSigner("test", bytes.Repeat([]byte{7}, 32))
	original := DevicePrincipal{DeviceID: 9, UserID: 42, DeviceUID: "device", PubKeySPKI: "public-key", DeviceActive: true, SecretVersion: 1, UserStatus: "ACTIVE"}
	claims := AccessClaims{Subject: 42, DeviceID: 9, SecretVersion: 1, KeyThumbprint: Thumbprint(original.PubKeySPKI), IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTokenTTL).Unix()}
	token, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		modify func(*DevicePrincipal)
		want   int
	}{
		{"matching", func(*DevicePrincipal) {}, http.StatusNoContent},
		{"other-user", func(p *DevicePrincipal) { p.UserID = 43 }, http.StatusUnauthorized},
		{"other-device", func(p *DevicePrincipal) { p.DeviceID = 10 }, http.StatusUnauthorized},
		{"revoked", func(p *DevicePrincipal) { p.DeviceActive = false }, http.StatusUnauthorized},
		{"rotated-key", func(p *DevicePrincipal) { p.PubKeySPKI = "another-key" }, http.StatusUnauthorized},
		{"rotated-secret", func(p *DevicePrincipal) { p.SecretVersion = 2 }, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := original
			tc.modify(&p)
			mw := Middleware{Signer: signer, Store: securityStore{p}, Clock: securityClock{now}}
			r := gin.New()
			r.GET("/protected", mw.S1(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("want %d got %d", tc.want, w.Code)
			}
		})
	}
}
func TestNonceInputBoundAndDeployedEncodings(t *testing.T) {
	for _, s := range []string{"h7Qm0x2Zk9rT4pWbJ1eYvA", "550e8400-e29b-41d4-a716-446655440000"} {
		if !validNonce(s) {
			t.Fatal("deployed nonce rejected")
		}
	}
	for _, s := range []string{"", "short", strings.Repeat("a", 129), "abcdefghijklmnop:else", "abcdefghijklmnop\n", "abcdefghijklmnop  ", "abcdefghijklmnop="} {
		if validNonce(s) {
			t.Fatal("invalid replay token accepted")
		}
	}
}
