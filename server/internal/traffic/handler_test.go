package traffic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/auth"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type testReader struct {
	userID int64
	days   int
	calls  int
	err    error
}

func (r *testReader) Daily(_ context.Context, userID int64, days int) (DailyUsage, error) {
	r.userID, r.days, r.calls = userID, days, r.calls+1
	return DailyUsage{Days: []Day{{Date: "2026-09-12", ByDevice: []DeviceUsage{}}}}, r.err
}

type testPrincipal struct{}

func (testPrincipal) LookupDevice(context.Context, int64) (auth.DevicePrincipal, error) {
	return auth.DevicePrincipal{DeviceID: 2, UserID: 7, PubKeySPKI: "test-public-key", DeviceActive: true, SecretVersion: 1, UserStatus: "ACTIVE"}, nil
}

func mountedHandler(t *testing.T, reader Reader) (*gin.Engine, string) {
	t.Helper()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	signer := auth.NewSigner("traffic-test", make([]byte, 32))
	token, err := signer.Sign(auth.AccessClaims{Subject: 7, DeviceID: 2, SecretVersion: 1,
		KeyThumbprint: auth.Thumbprint("test-public-key"), IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	h := Handler{Svc: reader, MW: &auth.Middleware{Signer: signer, Store: testPrincipal{}, Clock: fixedClock{now}}}
	h.Mount(r.Group("/v1"))
	return r, token
}

func TestDailyTrafficRequiresAuthenticationAndUsesPrincipal(t *testing.T) {
	reader := &testReader{}
	r, token := mountedHandler(t, reader)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/traffic/daily", nil))
	if w.Code != http.StatusUnauthorized || reader.calls != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("anonymous request reached service or was cacheable", w.Code, reader.calls)
	}
	for _, query := range []string{"?user_id=999&device_id=888", "?days=1&user_id=999", "?days=90"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/traffic/daily"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		wantDays := defaultDays
		if strings.Contains(query, "days=1") {
			wantDays = 1
		} else if strings.Contains(query, "days=90") {
			wantDays = 90
		}
		if w.Code != http.StatusOK || reader.userID != 7 || reader.days != wantDays || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, reader.userID, reader.days, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"by_device":[]`) || !strings.Contains(w.Body.String(), `"up_bytes":0`) {
			t.Fatal("empty arrays and zero bytes must remain explicit", w.Body.String())
		}
	}
}

func TestDailyTrafficRejectsInvalidRangesAndHidesErrors(t *testing.T) {
	reader := &testReader{}
	r, token := mountedHandler(t, reader)
	request := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/traffic/daily?"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, query := range []string{"days=", "days=0", "days=-1", "days=91", "days=1.5", "days=01", "days=%2B1", "days=1%20", "days=1&days=2", "days=1&days=1", "days=999999999999999999999", "days=%ZZ", "days=1;days=90"} {
		w := request(query)
		if w.Code != http.StatusBadRequest || reader.calls != 0 || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("invalid query reached reader: %q %d %d", query, w.Code, reader.calls)
		}
	}
	reader.err = errors.New("private database detail")
	w := request("days=30")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"days":`) {
		t.Fatal("error exposed internal details or partial traffic", w.Code, w.Body.String())
	}
}
