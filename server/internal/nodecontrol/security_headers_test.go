package nodecontrol

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNodeResponsesHaveSecurityHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("content type header = %q", got)
	}
	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("frame header = %q", got)
	}
	if got := recorder.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Fatalf("CSP = %q", got)
	}
}
