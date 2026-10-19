package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func TestWebSessionCookieIsStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeWebSessionCookie(c, "session-token-value", int(webSessionTTL.Seconds()))
	raw := w.Header().Get("Set-Cookie")
	for _, part := range []string{"yh_session=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(raw, part) {
			t.Fatalf("cookie missing %s: %s", part, raw)
		}
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "samesite=none") || strings.Contains(lower, "samesite=lax") || strings.Contains(raw, "Domain=") {
		t.Fatal(raw)
	}
	cleared := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(cleared)
	clearWebSessionCookie(c2)
	gone := cleared.Header().Get("Set-Cookie")
	if !strings.Contains(gone, "Max-Age=0") || !strings.Contains(gone, "HttpOnly") || !strings.Contains(gone, "Secure") || !strings.Contains(gone, "SameSite=Strict") {
		t.Fatal(gone)
	}
}

func TestWebLoginFailureMessageIsUniform(t *testing.T) {
	if !webLoginLimited(webLoginFailPerAddress, 0) || !webLoginLimited(0, webLoginFailPerIP) || webLoginLimited(webLoginFailPerAddress-1, webLoginFailPerIP-1) {
		t.Fatal("threshold")
	}
	msg := httpx.NewError(httpx.ErrLoginInvalid, msgWebLoginInvalid).Error()
	if strings.Contains(msg, "不存在") || strings.Contains(msg, "未注册") || strings.Contains(msg, "停用") {
		t.Fatal(msg)
	}
}

func TestWebLoginRejectsForeignOrigin(t *testing.T) {
	r := newWebEngine(t, &Service{Clock: clock.Real{}}, nil)
	for _, origin := range []string{"https://evil.example", webWWWOrigin, "http://yhvpn.io"} {
		w := webDo(r, http.MethodPost, "/v1/auth/web/login", "203.0.113.20", origin, []byte(`{"address":"T","password":"x"}`), "")
		if w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%s: %d %q %s", origin, w.Code, w.Header().Get("Access-Control-Allow-Origin"), w.Body.String())
		}
	}
}

func TestWebSessionRoundTripAndLogout(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	ctx := context.Background()
	token, err := svc.IssueWebSession(ctx, 42)
	if err != nil || token == "" {
		t.Fatal(err)
	}
	id, err := svc.WebSessionUser(ctx, token)
	if err != nil || id != 42 {
		t.Fatalf("session %d %v", id, err)
	}
	if _, err := svc.WebSessionUser(ctx, "not-a-session"); codeOf(err) != httpx.ErrTokenMissing {
		t.Fatal(err)
	}
	if err := svc.RevokeWebSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WebSessionUser(ctx, token); codeOf(err) != httpx.ErrTokenMissing {
		t.Fatalf("revoked session still valid: %v", err)
	}
}

func TestWebLoginFailureCounters(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	ctx := context.Background()
	lookup := "addr-lookup-" + testIPv4(t)
	ipHash := "ip-hash-" + testIPv4(t)
	for i := 0; i < webLoginFailPerAddress-1; i++ {
		if err := svc.noteWebLoginFailure(ctx, lookup, "scratch-ip"); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := svc.webLoginBlocked(ctx, lookup, "fresh-ip")
	if err != nil || blocked {
		t.Fatalf("early block %v %v", blocked, err)
	}
	if err := svc.noteWebLoginFailure(ctx, lookup, "another-ip"); err != nil {
		t.Fatal(err)
	}
	blocked, err = svc.webLoginBlocked(ctx, lookup, "fresh-ip")
	if err != nil || !blocked {
		t.Fatalf("address limit %v %v", blocked, err)
	}
	for i := 0; i < webLoginFailPerIP; i++ {
		if err := svc.noteWebLoginFailure(ctx, lookup+"-other", ipHash); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err = svc.webLoginBlocked(ctx, "brand-new-address", ipHash)
	if err != nil || !blocked {
		t.Fatalf("ip limit %v %v", blocked, err)
	}
}

func TestWebLoginBadProofDoesNotTouchAccounts(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	r := newWebEngine(t, svc, rdb)
	ip := testIPv4(t)
	ch := mustIssue(t, r, ip)
	counter := solveWebPoW(t, ch.ChallengeID, ch.Nonce, ch.Difficulty) + 1
	if counter > webPoWMaxCounter || webPoWAccept(ch.ChallengeID, ch.Nonce, counter, ch.Difficulty) {
		counter = 0
		if webPoWAccept(ch.ChallengeID, ch.Nonce, counter, ch.Difficulty) {
			t.Fatal("could not pick a failing counter")
		}
	}
	body, err := json.Marshal(map[string]any{
		"address": testAddress(t), "password": "abc12345",
		"challenge_id": ch.ChallengeID, "counter": counter,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := webDo(r, http.MethodPost, "/v1/auth/web/login", ip, webRegisterOrigin, body, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad proof: %d %s", w.Code, w.Body.String())
	}
	assertWebCORS(t, w)
	code, msg := errorMessage(t, w)
	if code != int(httpx.ErrBadRequest) || (msg != msgWebChallengeBad && msg != msgWebChallengeExpired) {
		t.Fatalf("message %d %s", code, msg)
	}
	if strings.Contains(w.Body.String(), "不存在") || strings.Contains(w.Body.String(), "未注册") {
		t.Fatal(w.Body.String())
	}
}

func TestWebAccountRequiresSession(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	r := newWebEngine(t, svc, rdb)
	w := webDo(r, http.MethodGet, "/v1/auth/web/account", testIPv4(t), webRegisterOrigin, nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("account: %d %s", w.Code, w.Body.String())
	}
	assertWebCORS(t, w)
	if code, msg := errorMessage(t, w); code != int(httpx.ErrTokenMissing) || msg != msgWebLoginRequired {
		t.Fatalf("%d %s", code, msg)
	}
}
