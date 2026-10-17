package httpx

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeadersFailClosedForJSONAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders(true))
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	req := httptest.NewRequest("GET", "/healthz", nil)
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	for key, want := range map[string]string{
		"Cache-Control":             "no-store",
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Permissions-Policy":        "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := res.Header().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestFailureAndPanicDoNotLeakSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "CANARY_password_totp_token_private_key"
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	r := gin.New()
	r.Use(RequestIDMiddleware(), RecoveryMiddleware())
	r.GET("/raw", func(c *gin.Context) { Fail(c, errors.New(secret)) })
	r.GET("/wrapped", func(c *gin.Context) { Fail(c, Internal.Wrap(errors.New(secret))) })
	r.GET("/details", func(c *gin.Context) {
		Fail(c, NewError(ErrInternal, secret).WithDetails(map[string]any{"secret": secret}))
	})
	r.GET("/panic", func(c *gin.Context) { panic(secret) })
	r.GET("/unavailable", func(c *gin.Context) {
		Fail(c, NewError(ErrNodeUnavailable, secret).WithDetails(map[string]any{"secret": secret}))
	})
	for _, path := range []string{"/raw", "/wrapped", "/details", "/panic"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 500 || strings.Contains(w.Body.String(), secret) || strings.Contains(logs.String(), secret) {
			t.Fatalf("secret leaked or wrong response for %s", path)
		}
		if !strings.Contains(w.Body.String(), "50001") || w.Header().Get(HeaderRequestID) == "" {
			t.Fatal("missing diagnostic reference")
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/unavailable", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "50302") || strings.Contains(w.Body.String(), secret) {
		t.Fatal("unavailable contract changed or secret leaked")
	}
}

func TestSecurityHeadersOmitHSTSOutsideProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SecurityHeaders(false))
	r.GET("/healthz", func(c *gin.Context) { c.Status(204) })
	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest("GET", "/healthz", nil))
	if got := res.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("development response unexpectedly enabled HSTS: %q", got)
	}
}
