package safelog

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorsNeverSerializeSecretText(t *testing.T) {
	const secret = "SECRET_CANARY_PASSWORD_TOTP_TOKEN_PRIVATEKEY"
	for _, err := range []error{errors.New(secret), fmt.Errorf("dsn %s: %w", secret, &pgconn.PgError{Code: "23505", Message: secret, Detail: secret}), &pgconn.PgError{Code: secret}} {
		var out bytes.Buffer
		slog.New(slog.NewJSONHandler(&out, nil)).Error("failed", "error", Error(err))
		if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "type") {
			t.Fatal("unsafe diagnostic", out.String())
		}
	}
}
