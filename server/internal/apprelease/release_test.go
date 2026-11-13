package apprelease

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

type fakeReleases struct {
	item Release
}

func (f fakeReleases) Current(context.Context, string) (Release, error) { return f.item, nil }
func (f fakeReleases) List(context.Context, string) ([]Release, error)  { return nil, nil }
func (f fakeReleases) Publish(context.Context, Input) (Release, error)  { return f.item, nil }
func (f fakeReleases) Rollback(context.Context, int64) (Release, error) { return f.item, nil }

func TestDownloadRedirectAllowsHead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{Svc: fakeReleases{item: Release{URL: "https://yhvpn.io/download/builds/YuhaiVPN-Windows-1.1.0.exe"}}}
	r := gin.New()
	r.GET("/v1/app/download/:platform", h.Redirect)
	r.HEAD("/v1/app/download/:platform", h.Redirect)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "/v1/app/download/windows", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s status %d", method, rec.Code)
		}
		if got := rec.Header().Get("Location"); got != h.Svc.(fakeReleases).item.URL {
			t.Fatalf("%s location %q", method, got)
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=60" {
			t.Fatalf("%s cache %q", method, rec.Header().Get("Cache-Control"))
		}
		body, _ := io.ReadAll(rec.Body)
		if method == http.MethodHead && len(body) != 0 {
			t.Fatalf("HEAD body %q", body)
		}
		if method == http.MethodGet && len(body) == 0 {
			t.Fatal("GET redirect had no body")
		}
	}
}

func TestAllowedURL(t *testing.T) {
	ok := []string{
		"https://yhvpn.io/download/builds/YuhaiVPN-Windows-1.1.0.exe",
		"https://www.yhvpn.io/download/latest/android",
		"https://api.yhvpn.io/v1/app/download/windows",
	}
	for _, raw := range ok {
		if !AllowedURL(raw) {
			t.Fatalf("expected allowed: %s", raw)
		}
	}
	bad := []string{
		"http://yhvpn.io/a.exe",
		"https://evil.yhvpn.io/a.exe",
		"https://yhvpn.io.evil.com/a.exe",
		"https://user:pass@yhvpn.io/a.exe",
		"https://yhvpn.io:8443/a.exe",
		"https://example.com/a.exe",
	}
	for _, raw := range bad {
		if AllowedURL(raw) {
			t.Fatalf("expected rejected: %s", raw)
		}
	}
}

func TestTokenOK(t *testing.T) {
	if tokenOK("Bearer secret-token", "") || tokenOK("", "secret-token") || tokenOK("Bearer other-token", "secret-token") {
		t.Fatal("empty or wrong token was accepted")
	}
	if !tokenOK("Bearer secret-token", "secret-token") {
		t.Fatal("matching token was rejected")
	}
}
