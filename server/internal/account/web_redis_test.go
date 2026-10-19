package account

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func TestWebChallengeCaptchaAndHoneypot(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	r := newWebEngine(t, svc, rdb)

	powIP := testIPv4(t)
	powChallenge := mustIssue(t, r, powIP)
	if powChallenge.Algorithm != webPoWAlgorithm || powChallenge.Difficulty != webPoWBits {
		t.Fatalf("challenge payload: %+v", powChallenge)
	}
	wrong := solveWebPoW(t, powChallenge.ChallengeID, powChallenge.Nonce, powChallenge.Difficulty) + 1
	if wrong > webPoWMaxCounter || webPoWAccept(powChallenge.ChallengeID, powChallenge.Nonce, wrong, powChallenge.Difficulty) {
		wrong = 0
	}
	wrongBody, _ := json.Marshal(map[string]any{
		"address": testAddress(t), "password": "abc12345",
		"challenge_id": powChallenge.ChallengeID, "counter": wrong,
	})
	wrongRes := webDo(r, http.MethodPost, "/v1/auth/web/register", powIP, webRegisterOrigin, wrongBody, "")
	if wrongRes.Code != http.StatusBadRequest || wrongRes.Header().Get("Access-Control-Allow-Origin") != webRegisterOrigin {
		t.Fatalf("bad pow: %d %s", wrongRes.Code, wrongRes.Body.String())
	}
	assertWebCORS(t, wrongRes)
	if code, msg := errorMessage(t, wrongRes); code != int(httpx.ErrBadRequest) || msg != msgWebChallengeBad {
		t.Fatalf("bad pow message: %d %s", code, msg)
	}

	// 蜜罐：解答是对的，但隐藏字段有值。这里没有数据库，若真去建账户会 panic。
	hpIP := testIPv4(t)
	hpChallenge := mustIssue(t, r, hpIP)
	hpCounter := solveWebPoW(t, hpChallenge.ChallengeID, hpChallenge.Nonce, hpChallenge.Difficulty)
	hpBody, _ := json.Marshal(map[string]any{
		"address": testAddress(t), "password": "abc12345", "yh_hp": "filled-by-bot",
		"challenge_id": hpChallenge.ChallengeID, "counter": hpCounter,
	})
	hpRes := webDo(r, http.MethodPost, "/v1/auth/web/register", hpIP, webRegisterOrigin, hpBody, "")
	if hpRes.Code != http.StatusBadRequest {
		t.Fatalf("honeypot: %d %s", hpRes.Code, hpRes.Body.String())
	}
	if _, msg := errorMessage(t, hpRes); msg != msgWebChallengeBad {
		t.Fatalf("honeypot message %s", msg)
	}
	again := webDo(r, http.MethodPost, "/v1/auth/web/register", hpIP, webRegisterOrigin, hpBody, "")
	if again.Code != http.StatusBadRequest {
		t.Fatalf("honeypot reuse: %d %s", again.Code, again.Body.String())
	}
	if _, msg := errorMessage(t, again); msg != msgWebChallengeExpired {
		t.Fatalf("consumed challenge message %s", msg)
	}

	// 题目绑定签发时的 IP。
	bindIP := testIPv4(t)
	otherIP := testIPv4(t)
	bound := mustIssue(t, r, bindIP)
	okCounter := solveWebPoW(t, bound.ChallengeID, bound.Nonce, bound.Difficulty)
	moved, _ := json.Marshal(map[string]any{
		"address": testAddress(t), "password": "abc12345", "challenge_id": bound.ChallengeID, "counter": okCounter,
	})
	movedRes := webDo(r, http.MethodPost, "/v1/auth/web/register", otherIP, "", moved, "")
	if movedRes.Code != http.StatusBadRequest {
		t.Fatalf("ip mismatch: %d %s", movedRes.Code, movedRes.Body.String())
	}
	if err := svc.VerifyWebChallenge(t.Context(), bound.ChallengeID, okCounter, bindIP); err != nil {
		t.Fatal(err)
	}
	raw, err := rdb.Get(t.Context(), webChallengeKey(bound.ChallengeID))
	if err != nil {
		t.Fatal(err)
	}
	var rec webChallengeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatal(err)
	}
	rec.ExpiresAt = time.Now().Add(-time.Second).UTC()
	updated, _ := json.Marshal(rec)
	if err := rdb.SetEX(t.Context(), webChallengeKey(bound.ChallengeID), string(updated), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := svc.VerifyWebChallenge(t.Context(), bound.ChallengeID, okCounter, bindIP); codeOf(err) != httpx.ErrBadRequest {
		t.Fatal(err)
	}
}

func TestWebRegisterRateLimit(t *testing.T) {
	rdb := openRedis(t)
	svc := &Service{Redis: rdb, Clock: clock.Real{}}
	r := newWebEngine(t, svc, rdb)
	ip := testIPv4(t)
	body := []byte(`{"address":"not-an-address","password":"abc12345","challenge_id":"00000000-0000-0000-0000-000000000000","counter":0}`)

	for i := 0; i < 8; i++ {
		w := webDo(r, http.MethodOptions, "/v1/auth/web/register", ip, webRegisterOrigin, nil, "content-type")
		if w.Code != http.StatusNoContent {
			t.Fatalf("preflight %d: %d", i, w.Code)
		}
	}
	if w := webDo(r, http.MethodPost, "/v1/auth/web/challenge", ip, "", []byte(`{}`), ""); w.Code != http.StatusOK {
		t.Fatalf("challenge before limit: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 5; i++ {
		w := webDo(r, http.MethodPost, "/v1/auth/web/register", ip, "", body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	blocked := webDo(r, http.MethodPost, "/v1/auth/web/register", ip, "", body, "")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("ip limit: %d retry=%q %s", blocked.Code, blocked.Header().Get("Retry-After"), blocked.Body.String())
	}
	if code, _ := errorMessage(t, blocked); code != int(httpx.ErrRateLimited) {
		t.Fatal(code)
	}

	// 伪造的 X-Forwarded-For 不能换一个限流桶。
	reqIP := testIPv4(t)
	for i := 0; i < 5; i++ {
		w := postRegisterForwarded(r, reqIP, fmt.Sprintf("203.0.113.%d", i+1), body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("forged setup %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	forged := postRegisterForwarded(r, reqIP, "198.51.100.20", body)
	if forged.Code != http.StatusTooManyRequests {
		t.Fatalf("forged XFF bypassed rate limit: %d %s", forged.Code, forged.Body.String())
	}

	var prefix [2]byte
	if _, err := rand.Read(prefix[:]); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 30; n++ {
		one := fmt.Sprintf("198.%d.%d.%d", int(prefix[0]), int(prefix[1]), n)
		w := webDo(r, http.MethodPost, "/v1/auth/web/register", one, "", body, "")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("prefix %s: %d %s", one, w.Code, w.Body.String())
		}
	}
	overflow := fmt.Sprintf("198.%d.%d.31", int(prefix[0]), int(prefix[1]))
	w := webDo(r, http.MethodPost, "/v1/auth/web/register", overflow, "", body, "")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("prefix limit: %d %s", w.Code, w.Body.String())
	}
}

func mustIssue(t *testing.T, h http.Handler, ip string) WebChallenge {
	t.Helper()
	w := webDo(h, http.MethodPost, "/v1/auth/web/challenge", ip, webRegisterOrigin, []byte(`{}`), "")
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != webRegisterOrigin {
		t.Fatalf("issue: %d %q %s", w.Code, w.Header().Get("Access-Control-Allow-Origin"), w.Body.String())
	}
	var env struct {
		Data WebChallenge `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func postRegisterForwarded(h http.Handler, remote, forwarded string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/web/register", bytes.NewReader(body))
	req.RemoteAddr = remote + ":12345"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", forwarded)
	req.Header.Set("X-Real-Ip", forwarded)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
