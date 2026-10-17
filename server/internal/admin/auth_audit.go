package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const (
	auditLogin  = "auth.login"
	auditMFA    = "auth.mfa"
	auditLogout = "auth.logout"
	auditStepUp = "auth.step_up"
)

type authRequestKey struct{}
type authRequest struct{ requestID, ip string }

var safeAuthRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Metadata originates from server middleware, never from the request body,
// cookie, credentials, or an arbitrary client-supplied request-id header.
func withAuthRequest(ctx context.Context, requestID, ip string) context.Context {
	if !safeAuthRequestID.MatchString(requestID) {
		requestID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return context.WithValue(ctx, authRequestKey{}, authRequest{requestID, ip})
}

func authEvent(ctx context.Context, action, ip string) audit.Event {
	m, ok := ctx.Value(authRequestKey{}).(authRequest)
	if !ok {
		ctx = withAuthRequest(ctx, "", ip)
		m = ctx.Value(authRequestKey{}).(authRequest)
	}
	if ip == "" {
		ip = m.ip
	}
	if addr, err := netip.ParseAddr(ip); err == nil && addr.Zone() == "" {
		ip = addr.Unmap().String()
	} else {
		ip = ""
	}
	return audit.Event{ActionCode: action, TargetType: "admin_user", RequestID: m.requestID, IP: ip}
}

func (s *Service) authAuditor() ApprovalAuditor {
	if s.Audit != nil {
		return s.Audit
	}
	// A missing override never disables mandatory auditing (including services
	// constructed by tests rather than New).
	return &audit.Service{Pool: s.Pool, Clock: s.Clock}
}

func (s *Service) authSuccess(ctx context.Context, tx pgx.Tx, e audit.Event, status string) error {
	e.Result = "SUCCESS"
	e.AfterData, _ = json.Marshal(map[string]string{"status": status})
	return s.authAuditor().Append(ctx, tx, e)
}

// Only fixed public errors survive. DB errors can contain failing rows, so
// neither their messages nor wrapped causes may reach HTTP logging or audit.
func safeAuthError(err error) *httpx.Error {
	var e *httpx.Error
	if errors.As(err, &e) {
		switch e.Code {
		case httpx.ErrBadRequest:
			return httpx.NewError(e.Code, "参数格式错误")
		case httpx.ErrSecretInvalid:
			return errCredentials
		case httpx.ErrTOTPInvalid:
			return errMFA
		case httpx.ErrTokenMissing:
			return errSession
		case httpx.ErrStepUpInvalid:
			return errStepUp
		case httpx.ErrForbidden:
			return httpx.NewError(e.Code, "安全验证未通过或权限不足")
		case httpx.ErrRateLimited:
			return errLimited
		}
	}
	return httpx.Internal
}

// Call only after the business transaction has ended. Failed credentials have
// already committed their counter; audit failure must not undo that budget.
// Detaching cancellation gives a disconnected client no way to suppress the
// failure event, while bounding the independent transaction to five seconds.
func (s *Service) authFailure(ctx context.Context, e audit.Event, cause error) error {
	public := safeAuthError(cause)
	e.Result = "DENIED"
	if public.Code.HTTPStatus() >= 500 {
		e.Result = "FAILED"
	}
	e.BeforeData = nil
	e.AfterData, _ = json.Marshal(map[string]string{"error_code": strconv.Itoa(int(public.Code))})
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return httpx.Internal
	}
	defer rollbackAuth(tx)
	if err = s.authAuditor().Append(ctx, tx, e); err != nil {
		return httpx.Internal
	}
	if err = tx.Commit(ctx); err != nil {
		return httpx.Internal
	}
	return public
}

func rollbackAuth(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func persistCredentialFailure(ctx context.Context, tx pgx.Tx, u adminUser, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := failed(ctx, tx, u, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
