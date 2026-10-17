package account

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func TestRemoveDeviceRejectsInvalidIDsAndMissingIdempotency(t *testing.T) {
	h := &Handler{Svc: &Service{}}
	r := gin.New()
	r.DELETE("/devices/:device_id", func(c *gin.Context) { httpx.SetPrincipal(c, 7, 2) }, h.removeDevice)
	for _, id := range []string{"0", "-1", "+3", "03", "abc", "9223372036854775808"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/devices/"+id, nil)
		req.Header.Set("Idempotency-Key", uuid.NewString())
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":40001`) {
			t.Fatalf("accepted invalid device ID %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	for _, key := range []string{"", "not-a-uuid"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/devices/3", nil)
		req.Header.Set("Idempotency-Key", key)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid idempotency key: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestRemoveDeviceRequiresAuthenticationAndRejectsCurrentDevice(t *testing.T) {
	h := &Handler{MW: &auth.Middleware{Clock: clock.Real{}}}
	r := gin.New()
	h.Mount(r.Group("/v1"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/v1/devices/3", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated removal was accepted: %d %s", w.Code, w.Body.String())
	}
	svc := &Service{}
	expectAuthError(t, svc.RemoveDevice(context.Background(), 7, 2, 2, ""), httpx.ErrBadRequest)
	expectAuthError(t, svc.RemoveDevice(context.Background(), 0, 2, 3, ""), httpx.ErrTokenMissing)
	expectAuthError(t, svc.RemoveDevice(context.Background(), 7, 0, 3, ""), httpx.ErrTokenMissing)
	expectAuthError(t, svc.RemoveDevice(context.Background(), 7, 2, 0, ""), httpx.ErrBadRequest)
}
