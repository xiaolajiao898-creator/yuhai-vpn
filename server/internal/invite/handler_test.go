package invite

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
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
	limit  int
	before int64
	calls  int
	err    error
}

func (r *testReader) Summary(_ context.Context, id int64) (Info, error) {
	r.userID, r.calls = id, r.calls+1
	return Info{InviteCode: "ABC234"}, r.err
}
func (r *testReader) Rewards(_ context.Context, id int64, limit int, before int64) (RewardPage, error) {
	r.userID, r.limit, r.before, r.calls = id, limit, before, r.calls+1
	return RewardPage{Items: []Reward{}}, r.err
}

type testPrincipal struct{}

func (testPrincipal) LookupDevice(context.Context, int64) (auth.DevicePrincipal, error) {
	return auth.DevicePrincipal{DeviceID: 2, UserID: 7, PubKeySPKI: "test-public-key", DeviceActive: true, SecretVersion: 1, UserStatus: "ACTIVE"}, nil
}

func mountedHandler(t *testing.T, reader Reader) (*gin.Engine, string) {
	t.Helper()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	signer := auth.NewSigner("invite-test", make([]byte, 32))
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

func TestMountRequiresAuthenticationAndScopesIdentity(t *testing.T) {
	reader := &testReader{}
	r, token := mountedHandler(t, reader)
	for _, path := range []string{"/v1/invite", "/v1/invite/rewards"} {
		priorCalls := reader.calls
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 || reader.calls != priorCalls {
			t.Fatalf("anonymous request reached service: %d %d", w.Code, reader.calls)
		}
		req := httptest.NewRequest("GET", path+"?user_id=999", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 || reader.userID != 7 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, reader.userID, w.Body.String())
		}
	}
	if reader.limit != 20 || reader.before != 0 {
		t.Fatal("unexpected default pagination", reader.limit, reader.before)
	}
}

func TestRewardCursorValidation(t *testing.T) {
	for _, raw := range []string{"limit=0", "limit=101", "limit=abc", "limit=1&limit=2", "cursor=", "cursor=invalid", "cursor=" + rewardCursor(8, "3"), "cursor=" + rewardCursor(7, "-1"), "cursor=" + rewardCursor(7, "0"), "cursor=" + rewardCursor(7, "9223372036854775808"), "cursor=" + strings.Repeat("x", 129), "cursor=" + rewardCursor(7, "3") + "&cursor=" + rewardCursor(7, "3")} {
		q, _ := url.ParseQuery(raw)
		if _, _, err := rewardParams(q, 7); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	limit, before, err := rewardParams(url.Values{"cursor": {rewardCursor(7, "456")}, "limit": {"2"}}, 7)
	if err != nil || limit != 2 || before != 456 {
		t.Fatal(limit, before, err)
	}
}

func TestRewardBadPaginationAndPrivateErrors(t *testing.T) {
	reader := &testReader{}
	r, token := mountedHandler(t, reader)
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := request("/v1/invite/rewards?cursor=" + rewardCursor(999, "3")); w.Code != 400 || reader.calls != 0 {
		t.Fatal("foreign cursor queried service", w.Code, reader.calls)
	}
	if w := request("/v1/invite/rewards"); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"next_cursor":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	reader.err = errors.New("private database error")
	for _, path := range []string{"/v1/invite", "/v1/invite/rewards"} {
		w := request(path)
		if w.Code != 500 || strings.Contains(w.Body.String(), "private database error") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestInviteURLRequiresExplicitTrustedOrigin(t *testing.T) {
	for _, base := range []string{"", "http://example.com", "javascript:alert(1)", "https://user:pass@example.com", "https://example.com/path", "https://example.com/?redirect=evil", "https://example.com/?", "https://example.com/#fragment", "//example.com", "https://"} {
		if inviteURL(base, "ABC234") != nil {
			t.Errorf("accepted invalid website origin %q", base)
		}
	}
	if inviteURL("https://example.com", "../ABC") != nil {
		t.Fatal("accepted invalid code")
	}
	link := inviteURL("https://example.com/", "ABC234")
	if link == nil || *link != "https://example.com/i/ABC234" {
		t.Fatal(link)
	}
}
