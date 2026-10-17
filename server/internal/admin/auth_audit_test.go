package admin

import (
	"context"
	"errors"
	"github.com/yuhai-vpn/server/internal/httpx"
	"strings"
	"testing"
)

func TestAuthAuditSafeMetadataAndErrors(t *testing.T) {
	ctx := withAuthRequest(context.Background(), "Bearer secret\n", "::ffff:192.0.2.5")
	e := authEvent(ctx, auditMFA, "")
	if !safeAuthRequestID.MatchString(e.RequestID) || e.IP != "192.0.2.5" || e.UserAgent != "" || e.AdminUserID != nil || e.TargetID != nil {
		t.Fatal("unsafe metadata")
	}
	if e := authEvent(context.Background(), auditLogin, "invalid-ip-token"); e.IP != "" {
		t.Fatal("invalid IP copied")
	}
	for _, in := range []error{errors.New("password=hidden row"), httpx.NewError(httpx.ErrBadRequest, "secret=hidden").WithDetails(map[string]any{"token": "hidden"}).Wrap(errors.New("password=hidden"))} {
		out := safeAuthError(in)
		if strings.Contains(out.Error(), "hidden") || out.Details != nil || errors.Unwrap(out) != nil {
			t.Fatal("raw failure escaped")
		}
	}
}
