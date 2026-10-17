package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientIPIgnoresForgedHeadersForRateLimitIdentity(t *testing.T) {
	r := gin.New()
	// Match the public app API's default: no proxy may assert a client address.
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.GET("/identity", func(c *gin.Context) { c.String(http.StatusOK, ClientIP(c)) })
	for _, remote := range []struct{ addr, ip string }{
		{"203.0.113.8:12345", "203.0.113.8"},
		{"[2001:db8::8]:12345", "2001:db8::8"},
	} {
		for _, hash := range []string{"", "new-bucket-1", "new-bucket-2", "198.51.100.7"} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/identity", nil)
			req.RemoteAddr = remote.addr
			req.Header.Set("X-Client-Ip-Hash", hash)
			req.Header.Set("X-Forwarded-For", "198.51.100.1, 198.51.100.2")
			req.Header.Set("X-Real-Ip", "198.51.100.3")
			req.Header.Set("Forwarded", "for=198.51.100.4")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != remote.ip {
				t.Fatalf("forged headers changed rate-limit identity: status=%d ip=%q want=%q", w.Code, w.Body.String(), remote.ip)
			}
		}
	}
}

func TestClientIPRespectsExplicitTrustedProxyBoundary(t *testing.T) {
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1/32"}); err != nil {
		t.Fatal(err)
	}
	r.GET("/identity", func(c *gin.Context) { c.String(http.StatusOK, ClientIP(c)) })
	for _, tc := range []struct{ name, remote, want string }{
		{"trusted proxy", "127.0.0.1:12345", "198.51.100.10"},
		{"untrusted client", "203.0.113.8:12345", "203.0.113.8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/identity", nil)
			req.RemoteAddr = tc.remote
			req.Header.Set("X-Forwarded-For", "198.51.100.10")
			req.Header.Set("X-Client-Ip-Hash", "attacker-chosen-bucket")
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != tc.want {
				t.Fatalf("incorrect proxy boundary: status=%d ip=%q want=%q", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}
