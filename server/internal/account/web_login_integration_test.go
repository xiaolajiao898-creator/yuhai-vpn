package account

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redeem"
	"github.com/yuhai-vpn/server/internal/redisx"
	"github.com/yuhai-vpn/server/internal/trial"
)

func TestWebLoginSessionAccountAndLogout(t *testing.T) {
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_REDIS_URL to isolated test services")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rdb, err := redisx.New(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := fieldenc.New("web-login", key)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Real{}
	svc := &Service{Repo: &Repo{Pool: pool}, Signer: auth.NewSigner("web-login", key), Enc: enc, Redis: rdb, Clock: clk, Pepper: key}
	h := &Handler{Svc: svc, MW: &auth.Middleware{Redis: rdb, Clock: clk}, Trials: &trial.Service{Pool: pool, Clock: clk}, Redeems: &redeem.Service{Pool: pool, Clock: clk}}
	engine := newAccountEngine(t, h)

	address := testAddress(t)
	password := "WebLogin99"
	ip := testIPv4(t)
	regBody := registerBody(t, engine, ip, webRegisterOrigin, address, password, "")
	reg := webDo(engine, http.MethodPost, "/v1/auth/web/register", ip, webRegisterOrigin, regBody, "")
	if reg.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", reg.Code, reg.Body.String())
	}

	unknownIP := testIPv4(t)
	unknown := postWebLogin(t, engine, unknownIP, testAddress(t), password)
	wrongIP := testIPv4(t)
	wrong := postWebLogin(t, engine, wrongIP, address, "Wrong999")
	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("unknown %d wrong %d", unknown.Code, wrong.Code)
	}
	uc, um := errorMessage(t, unknown)
	wc, wm := errorMessage(t, wrong)
	if uc != int(httpx.ErrLoginInvalid) || wc != uc || um != wm || um != msgWebLoginInvalid {
		t.Fatalf("messages %d %q / %d %q", uc, um, wc, wm)
	}
	if strings.Contains(unknown.Body.String(), "不存在") || strings.Contains(wrong.Body.String(), "未注册") {
		t.Fatal("existence leaked")
	}
	assertWebCORS(t, unknown)

	ok := postWebLogin(t, engine, testIPv4(t), "  "+address+"  ", password)
	if ok.Code != http.StatusOK {
		t.Fatalf("login: %d %s", ok.Code, ok.Body.String())
	}
	assertWebCORS(t, ok)
	cookie := ok.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookie {
		if c.Name == webSessionCookieName {
			session = c
		}
	}
	rawCookie := ok.Header().Get("Set-Cookie")
	if session == nil || session.Value == "" || !strings.Contains(rawCookie, "HttpOnly") || !strings.Contains(rawCookie, "Secure") || !strings.Contains(rawCookie, "SameSite=Strict") {
		t.Fatalf("cookie %v raw %s", session, rawCookie)
	}
	if strings.Contains(ok.Body.String(), session.Value) {
		t.Fatal("session token leaked into JSON")
	}

	account := webDoCookie(engine, http.MethodGet, "/v1/auth/web/account", testIPv4(t), session)
	if account.Code != http.StatusOK {
		t.Fatalf("account: %d %s", account.Code, account.Body.String())
	}
	var view struct {
		Data WebAccount `json:"data"`
	}
	if err := json.Unmarshal(account.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Data.Account != address || view.Data.PlanName == "" || view.Data.DeviceCount != 0 {
		t.Fatalf("account payload %+v", view.Data)
	}

	noCookie := webDo(engine, http.MethodGet, "/v1/auth/web/account", testIPv4(t), webRegisterOrigin, nil, "")
	if noCookie.Code != http.StatusUnauthorized {
		t.Fatalf("anon account %d", noCookie.Code)
	}

	trialRes := webDoCookie(engine, http.MethodPost, "/v1/auth/web/trial", testIPv4(t), session)
	if trialRes.Code != http.StatusOK {
		t.Fatalf("trial: %d %s", trialRes.Code, trialRes.Body.String())
	}
	again := webDoCookie(engine, http.MethodPost, "/v1/auth/web/trial", testIPv4(t), session)
	if again.Code != http.StatusOK || !strings.Contains(again.Body.String(), `"already"`) {
		t.Fatalf("trial replay: %d %s", again.Code, again.Body.String())
	}
	after := webDoCookie(engine, http.MethodGet, "/v1/auth/web/account", testIPv4(t), session)
	var afterView struct {
		Data WebAccount `json:"data"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &afterView); err != nil {
		t.Fatal(err)
	}
	if afterView.Data.PlanName != "免费试用" || afterView.Data.ExpireAt == nil || !afterView.Data.Active || afterView.Data.TrialEligible {
		t.Fatalf("after trial %+v", afterView.Data)
	}
	if afterView.Data.ExpireAt.Before(time.Now().Add(23*time.Hour)) || afterView.Data.ExpireAt.After(time.Now().Add(25*time.Hour)) {
		t.Fatalf("expire %s", afterView.Data.ExpireAt)
	}

	badCode := webDoCookieBody(engine, http.MethodPost, "/v1/auth/web/redeem", testIPv4(t), session, []byte(`{"code":"nope"}`))
	if badCode.Code != http.StatusBadRequest {
		t.Fatalf("bad code: %d %s", badCode.Code, badCode.Body.String())
	}
	missingCode := webDoCookieBody(engine, http.MethodPost, "/v1/auth/web/redeem", testIPv4(t), session, []byte(`{"code":"ABCD-EFGH-JKLM-NPQR"}`))
	if missingCode.Code != http.StatusNotFound || !strings.Contains(missingCode.Body.String(), "码不存在") {
		t.Fatalf("missing code: %d %s", missingCode.Code, missingCode.Body.String())
	}

	// 地址失败满额后，正确密码也先被挡住，而且提示不说明账户在不在。
	limitIP := testIPv4(t)
	var limited *httptest.ResponseRecorder
	for i := 0; i < webLoginFailPerAddress+1; i++ {
		limited = postWebLogin(t, engine, limitIP, address, "Wrong999")
	}
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("address limit: %d %s", limited.Code, limited.Body.String())
	}
	if _, msg := errorMessage(t, limited); msg != msgWebLoginLimited || strings.Contains(limited.Body.String(), address) {
		t.Fatal(limited.Body.String())
	}

	out := webDoCookie(engine, http.MethodPost, "/v1/auth/web/logout", testIPv4(t), session)
	if out.Code != http.StatusOK || !strings.Contains(out.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout: %d %s", out.Code, out.Header().Get("Set-Cookie"))
	}
	dead := webDoCookie(engine, http.MethodGet, "/v1/auth/web/account", testIPv4(t), session)
	if dead.Code != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d %s", dead.Code, dead.Body.String())
	}
}

func newAccountEngine(t *testing.T, h *Handler) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(httpx.RequestIDMiddleware())
	h.Mount(r.Group("/v1"))
	return r
}

func postWebLogin(t *testing.T, h http.Handler, ip, address, password string) *httptest.ResponseRecorder {
	t.Helper()
	ch := mustIssue(t, h, ip)
	counter := solveWebPoW(t, ch.ChallengeID, ch.Nonce, ch.Difficulty)
	body, err := json.Marshal(map[string]any{
		"address": address, "password": password,
		"challenge_id": ch.ChallengeID, "counter": counter,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := webDo(h, http.MethodPost, "/v1/auth/web/login", ip, webRegisterOrigin, body, "")
	return w
}

func webDoCookie(h http.Handler, method, path, ip string, cookie *http.Cookie) *httptest.ResponseRecorder {
	return webDoCookieBody(h, method, path, ip, cookie, []byte(`{}`))
}

func webDoCookieBody(h http.Handler, method, path, ip string, cookie *http.Cookie, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	req.RemoteAddr = ip + ":12345"
	req.Header.Set("Origin", webRegisterOrigin)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
