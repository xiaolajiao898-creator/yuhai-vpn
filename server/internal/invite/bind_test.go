package invite

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func requireBindError(t *testing.T, err error, code httpx.Code) {
	t.Helper()
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("wanted code %d, got %v", code, err)
	}
}

func TestBindingValidationAndNormalization(t *testing.T) {
	for _, in := range []BindInput{{"", "MANUAL"}, {"ABC012", "MANUAL"}, {"../ABC", "MANUAL"}, {"ABC234", "APK_CHANNEL"}, {"ABC234", ""}} {
		if _, err := normalizeBinding(in, false); err == nil {
			t.Fatalf("accepted invalid binding: %+v", in)
		}
	}
	if got, err := normalizeBinding(BindInput{" abc234 ", "MANUAL"}, false); err != nil || got.Code != "ABC234" {
		t.Fatal(got, err)
	}
	if _, err := normalizeBinding(BindInput{"ABC234", "APK_CHANNEL"}, true); err != nil {
		t.Fatal(err)
	}
	binder := &Binder{}
	_, err := binder.Bind(context.Background(), 0, BindInput{"ABC234", "MANUAL"}, uuid.NewString())
	requireBindError(t, err, httpx.ErrTokenMissing)
	_, err = binder.Bind(context.Background(), 7, BindInput{"ABC234", "MANUAL"}, "invalid")
	requireBindError(t, err, httpx.ErrBadRequest)
}

func TestBindingMountRequiresDeviceSignature(t *testing.T) {
	r, token := mountedHandler(t, &testReader{})
	for _, bearer := range []string{"", token} {
		req := httptest.NewRequest(http.MethodPost, "/v1/invite/bind", strings.NewReader(`{"code":"ABC234","method":"MANUAL"}`))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("binding accepted absent device signature: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestBindingHandlerRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	h := &Handler{Binder: &Binder{}}
	r := gin.New()
	r.POST("/bind", func(c *gin.Context) { httpx.SetPrincipal(c, 7, 2) }, h.bind)
	for _, body := range []string{`null`, `[]`, `{"code":"ABC234","method":"MANUAL","user_id":999}`, `{"code":"ABC234","method":"MANUAL"}{}`, `{"code":"ABC234","method":"APK_CHANNEL"}`} {
		req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", uuid.NewString())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body.String())
		}
	}
}
