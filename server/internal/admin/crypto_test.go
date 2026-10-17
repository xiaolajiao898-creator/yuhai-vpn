package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPasswordPolicy(t *testing.T) {
	hash, err := HashPassword("correct test password")
	if err != nil || !VerifyPassword(hash, "correct test password") {
		t.Fatal("password round trip", err)
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
	for _, v := range []string{strings.Replace(hash, "m=65536", "m=9999999999", 1), hash + "$extra", "$argon2i$", strings.Replace(hash, "v=19", "v=16", 1)} {
		if VerifyPassword(v, "correct test password") {
			t.Fatal("malformed or unsupported hash accepted")
		}
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short provisioning password accepted")
	}
}
func TestTOTPRFCAndReplay(t *testing.T) {
	// RFC 6238 SHA-1 secret and timestamps; six-digit form takes last six digits.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	vectors := []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}}
	for _, v := range vectors {
		step, ok := verifyTOTP(secret, v.code, time.Unix(v.unix, 0), 0)
		if !ok || step != v.unix/30 {
			t.Fatalf("RFC vector %d: %d %v", v.unix, step, ok)
		}
		if _, ok := verifyTOTP(secret, v.code, time.Unix(v.unix, 0), step); ok {
			t.Fatal("replayed step accepted")
		}
	}
	if _, ok := verifyTOTP(secret, "287082", time.Unix(89, 0), 0); !ok {
		t.Fatal("previous step missing")
	}
	if _, ok := verifyTOTP(secret, "287082", time.Unix(119, 0), 0); ok {
		t.Fatal("expired step accepted")
	}
	for _, code := range []string{"12345", "0000000", "０８１８０４", "abcdef"} {
		if _, ok := verifyTOTP(secret, code, time.Unix(59, 0), 0); ok {
			t.Fatal("invalid code accepted")
		}
	}
	if _, ok := verifyTOTP("bad", "287082", time.Unix(59, 0), 0); ok {
		t.Fatal("invalid secret accepted")
	}
}
func TestTokensCSRFAndNetwork(t *testing.T) {
	token, _ := randomToken()
	other, _ := randomToken()
	if !validToken(token) || tokenHash(token) == token || !validCSRF(token, csrfToken(token)) {
		t.Fatal("token protections failed")
	}
	if validCSRF(other, csrfToken(token)) || validCSRF(token, token) || validCSRF(token, "") {
		t.Fatal("wrong CSRF accepted")
	}
	if !sameNetwork("192.0.2.1", "192.0.2.200") || sameNetwork("192.0.2.1", "192.0.3.1") || !sameNetwork("::ffff:192.0.2.1", "192.0.2.2") || sameNetwork("bad", "bad") || sameNetwork("2001:db8:1::1", "2001:db8:2::1") {
		t.Fatal("IP prefix comparison failed")
	}
}
func TestLoginOrigin(t *testing.T) {
	h := Handler{SecureCookies: true}
	for _, tc := range []struct {
		origin, fetch string
		want          bool
	}{{"https://admin.example", "same-origin", true}, {"http://admin.example", "", false}, {"https://evil.example", "", false}, {"null", "", false}, {"", "cross-site", false}, {"", "", true}} {
		r := httptest.NewRequest("POST", "https://admin.example/admin/v1/auth/login", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.fetch)
		if h.sameOrigin(r) != tc.want {
			t.Fatalf("origin %q", tc.origin)
		}
	}
}

func TestHTTPGuardsBeforeCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{SecureCookies: true}
	h.Register(r.Group("/admin/v1"))
	cases := []struct {
		path, body, contentType, origin string
		status                          int
	}{
		{"/auth/login", `{"username":"example","password":"ignored"}`, "text/plain", "", 400},
		{"/auth/login", `{"username":"example","password":"ignored","extra":true}`, "application/json", "", 400},
		{"/auth/login", `{} {}`, "application/json", "", 400},
		{"/auth/login", `{"password":"` + strings.Repeat("x", 5000) + `"}`, "application/json", "", 400},
		{"/auth/login", `{}`, "application/json", "https://evil.example", 403},
		{"/auth/logout", `{}`, "application/json", "", 401},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("POST", "https://admin.example/admin/v1"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.contentType)
		req.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("guard %s status=%d expected=%d", tc.path, w.Code, tc.status)
		}
	}
}
