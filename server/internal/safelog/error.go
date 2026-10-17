// Package safelog produces diagnostics without serializing untrusted error text.
package safelog

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
)

var sqlState = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// Error preserves the error class and SQLSTATE for diagnosis. Driver errors,
// URLs, HTTP bodies and wrapped causes can contain credentials or private keys;
// never call their Error/String methods at an operational logging boundary.
func Error(err error) slog.Value {
	attrs := []slog.Attr{slog.String("type", fmt.Sprintf("%T", err))}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && sqlState.MatchString(pg.Code) {
		attrs = append(attrs, slog.String("sqlstate", pg.Code))
	}
	return slog.GroupValue(attrs...)
}
