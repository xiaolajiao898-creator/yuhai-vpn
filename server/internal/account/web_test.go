package account

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redisx"
)

func TestWebPoWMatchesFrozenPreimage(t *testing.T) {
	var zeros [32]byte
	if leadingZeroBits(zeros) != 256 {
		t.Fatal(leadingZeroBits(zeros))
	}
	zeros[0] = 0x0f
	if leadingZeroBits(zeros) != 4 {
		t.Fatal(leadingZeroBits(zeros))
	}
	zeros[0] = 0x80
	if leadingZeroBits(zeros) != 0 {
		t.Fatal(leadingZeroBits(zeros))
	}
	const id = "11111111-1111-1111-1111-111111111111"
	const nonce = "abc"
	sum := sha256.Sum256([]byte(id + ":" + nonce + ":0"))
	if hex.EncodeToString(sum[:]) != "fc7ef099a6e5f887497b2bf6810a91330da22704b00488389db9dd32006ae62a" {
		t.Fatalf("preimage digest changed: %x", sum)
	}
	if webPoWDigest(id, nonce, 0) != sum {
		t.Fatal("digest helper drifted from the documented preimage")
	}
	var easy int64 = -1
	for c := int64(0); c < 100000; c++ {
		bits := leadingZeroBits(webPoWDigest(id, nonce, c))
		if bits >= 8 && bits < webPoWBits {
			easy = c
			break
		}
	}
	if easy < 0 {
		t.Fatal("no easy solution in range")
	}
	if !webPoWAccept(id, nonce, easy, 8) || webPoWAccept(id, nonce, easy, webPoWBits) {
		t.Fatalf("difficulty check failed for counter %d", easy)
	}
	if webPoWAccept(id, nonce, -1, 8) || webPoWAccept(id, nonce, webPoWMaxCounter+1, 8) {
		t.Fatal("counter bounds")
	}
}

func TestWebCORSAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(httpx.RequestIDMiddleware())
	(&Handler{Svc: &Service{Clock: clock.Real{}}, MW: &auth.Middleware{}}).Mount(r.Group("/v1"))

	allowedPaths := []string{
		"/v1/auth/web/challenge", "/v1/auth/web/register", "/v1/auth/web/login",
		"/v1/auth/web/logout", "/v1/auth/web/account", "/v1/auth/web/trial", "/v1/auth/web/redeem",
	}
	for _, path := range allowedPaths {
		w := webDo(r, http.MethodOptions, path, "203.0.113.10", webRegisterOrigin, nil, "content-type")
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		assertWebCORS(t, w)
		if w.Header().Get("Access-Control-Allow-Methods") != "GET, POST, OPTIONS" {
			t.Fatal(w.Header().Get("Access-Control-Allow-Methods"))
		}
		if w.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
			t.Fatal(w.Header().Get("Access-Control-Allow-Headers"))
		}
	}
	getPreflight := webDoMethod(r, http.MethodOptions, "/v1/auth/web/account", "203.0.113.10", webRegisterOrigin, nil, "content-type", http.MethodGet)
	if getPreflight.Code != http.StatusNoContent {
		t.Fatalf("GET preflight: %d %s", getPreflight.Code, getPreflight.Body.String())
	}
	assertWebCORS(t, getPreflight)
	rejected := []string{
		"https://evil.example", "https://yhvpn.io.evil.com", "https://notyhvpn.io",
		"http://yhvpn.io", "https://yhvpn.io:443", "https://yhvpn.io/", "https://YHVPN.io",
		"null", "https://www.yhvpn.io.evil.com", webWWWOrigin,
	}
	for _, origin := range rejected {
		w := webDo(r, http.MethodOptions, "/v1/auth/web/register", "203.0.113.10", origin, nil, "content-type")
		if w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("options %s: %d %q", origin, w.Code, w.Header().Get("Access-Control-Allow-Origin"))
		}
		post := webDo(r, http.MethodPost, "/v1/auth/web/register", "203.0.113.11", origin, []byte(`{}`), "")
		if post.Code != http.StatusForbidden || post.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("post %s: %d %q %s", origin, post.Code, post.Header().Get("Access-Control-Allow-Origin"), post.Body.String())
		}
		var env struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(post.Body.Bytes(), &env); err != nil || env.Error.Code != int(httpx.ErrForbidden) {
			t.Fatalf("forbidden body: %v %s", err, post.Body.String())
		}
	}
	extra := webDo(r, http.MethodOptions, "/v1/auth/web/register", "203.0.113.10", webRegisterOrigin, nil, "content-type, authorization")
	if extra.Code != http.StatusForbidden || extra.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("extra header: %d %q", extra.Code, extra.Header().Get("Access-Control-Allow-Origin"))
	}
	// 其它接口保持原来的行为：没有 CORS 头，预检也不是 204。
	other := webDo(r, http.MethodOptions, "/v1/auth/register", "203.0.113.10", webRegisterOrigin, nil, "content-type")
	if other.Code == http.StatusNoContent || other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("app register gained CORS: %d %q", other.Code, other.Header().Get("Access-Control-Allow-Origin"))
	}
	get := webDo(r, http.MethodGet, "/v1/auth/web/register", "203.0.113.10", webRegisterOrigin, nil, "")
	if get.Code == http.StatusNoContent || get.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("GET should not be a CORS route: %d %q", get.Code, get.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestDeployedRegisterPageIsSelfContained(t *testing.T) {
	js, err := os.ReadFile("../../../deploy/site-patch/register/register.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := os.ReadFile("../../../deploy/site-patch/register/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(html, js) || !bytes.Contains(html, []byte(`<script src="/register/register.js"></script>`)) {
		t.Fatal("register/index.html must load register.js as an external script")
	}
	for _, needle := range []string{
		"TronLink", "imToken", "交易所", "yh_hp", "https://api.yhvpn.io",
		`id="address"`, `id="password"`, `id="password2"`, `id="invite"`, `id="submit"`,
		"下载安卓版", "下载 Windows 版",
	} {
		if !bytes.Contains(html, []byte(needle)) {
			t.Fatalf("register page missing %s", needle)
		}
	}
	for _, path := range []string{"../../../deploy/site-patch/index.html", "../../../deploy/site-patch/download/index.html"} {
		page, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(page, []byte(`href="/register/"`)) {
			t.Fatalf("%s has no register link", path)
		}
	}
	copy, err := os.ReadFile("../../../apps/website/public/assets/web-register.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(js, copy) {
		t.Fatal("apps/website copy of the solver drifted from deploy/site-patch/register/register.js")
	}
}

func webDo(h http.Handler, method, path, ip, origin string, body []byte, requestHeaders string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = ip + ":12345"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method == http.MethodOptions {
		req.Header.Set("Access-Control-Request-Method", "POST")
		if requestHeaders != "" {
			req.Header.Set("Access-Control-Request-Headers", requestHeaders)
		}
	} else if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func newWebEngine(t *testing.T, svc *Service, rdb *redisx.Client) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(httpx.RequestIDMiddleware())
	(&Handler{Svc: svc, MW: &auth.Middleware{Redis: rdb, Clock: clock.Real{}}}).Mount(r.Group("/v1"))
	return r
}

func testIPv4(t *testing.T) string {
	t.Helper()
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("198.%d.%d.10", int(b[0]), int(b[1]))
}

func openRedis(t *testing.T) *redisx.Client {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("set TEST_REDIS_URL to an isolated redis")
	}
	rdb, err := redisx.New(url)
	if err != nil {
		t.Fatal(err)
	}
	return rdb
}

func solveWebPoW(t *testing.T, id, nonce string, bits int) int64 {
	t.Helper()
	for c := int64(0); c <= webPoWMaxCounter; c++ {
		if webPoWAccept(id, nonce, c, bits) {
			return c
		}
	}
	t.Fatal("no proof-of-work solution")
	return 0
}

func errorMessage(t *testing.T, w *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("error json: %v %s", err, w.Body.String())
	}
	return env.Error.Code, env.Error.Message
}

func assertWebCORS(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	acao := w.Header().Get("Access-Control-Allow-Origin")
	if acao != webRegisterOrigin || strings.Contains(acao, "*") || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("cors origin=%q cred=%q headers=%v", acao, w.Header().Get("Access-Control-Allow-Credentials"), w.Header())
	}
}

func webDoMethod(h http.Handler, method, path, ip, origin string, body []byte, requestHeaders, requestMethod string) *httptest.ResponseRecorder {
	w := webDo(h, method, path, ip, origin, body, requestHeaders)
	if method == http.MethodOptions && requestMethod != "" && requestMethod != http.MethodPost {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = ip + ":12345"
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", requestMethod)
		if requestHeaders != "" {
			req.Header.Set("Access-Control-Request-Headers", requestHeaders)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}
	return w
}
