package account

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type ledgerRows struct {
	pgx.Rows
	values [][]any
	pos    int
	err    error
	closed bool
}

func (r *ledgerRows) Next() bool { r.pos++; return r.pos <= len(r.values) }
func (r *ledgerRows) Close()     { r.closed = true }
func (r *ledgerRows) Err() error { return r.err }
func (r *ledgerRows) Scan(dest ...any) error {
	for i, value := range r.values[r.pos-1] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func TestLedgerPaginationValidation(t *testing.T) {
	for _, raw := range []string{"limit=0", "limit=101", "limit=abc", "limit=1&limit=2", "cursor=", "cursor=invalid", "cursor=" + ledgerCursor("balance", 8, "3"), "cursor=" + ledgerCursor("entitlements", 7, "3"), "cursor=" + ledgerCursor("balance", 7, "-1")} {
		q, _ := url.ParseQuery(raw)
		if _, _, err := ledgerParams(q, "balance", 7); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	limit, before, err := ledgerParams(url.Values{}, "balance", 7)
	if err != nil || limit != 20 || before != 0 {
		t.Fatal(limit, before, err)
	}
}

func ledgerRequest(query ledgerQuery, kind, raw string, userID int64) *httptest.ResponseRecorder {
	r := gin.New()
	r.GET("/ledger", func(c *gin.Context) { httpx.SetPrincipal(c, userID, 2) }, serveLedger(kind, query))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/ledger"+raw, nil))
	return w
}

func TestLedgerScopesIdentityAndPaginates(t *testing.T) {
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	rows := &ledgerRows{values: [][]any{{int64(9), "DEPOSIT", int64(1000043), "", now}, {int64(8), "PURCHASE", int64(-1000000), "", now}, {int64(7), "DEPOSIT", int64(3), "", now}}}
	w := ledgerRequest(func(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
		if !strings.Contains(sql, "user_id=$1") || !reflect.DeepEqual(args, []any{int64(7), int64(0), 3}) {
			t.Fatalf("unsafe scope or limit: %s %v", sql, args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing query deadline")
		}
		return rows, nil
	}, "balance", "?limit=2&user_id=999", 7)
	if w.Code != 200 || !rows.closed || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Data ledgerPage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	p := body.Data
	if len(p.Items) != 2 || *p.Items[0].Amount != "1.000043" || *p.Items[1].Amount != "-1.000000" || p.NextCursor == nil {
		t.Fatal(p)
	}
	_, before, err := ledgerParams(url.Values{"cursor": {*p.NextCursor}}, "balance", 7)
	if err != nil || before != 8 {
		t.Fatal(before, err)
	}
}

func TestLedgerEmptyErrorsAndMissingIdentity(t *testing.T) {
	called := false
	query := func(context.Context, string, ...any) (pgx.Rows, error) { called = true; return &ledgerRows{}, nil }
	if w := ledgerRequest(query, "balance", "", 0); w.Code == 200 || called {
		t.Fatal("queried without identity")
	}
	if w := ledgerRequest(query, "balance", "?limit=500", 7); w.Code != 400 || called {
		t.Fatal("queried invalid pagination")
	}
	w := ledgerRequest(query, "balance", "", 7)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) || !strings.Contains(w.Body.String(), `"next_cursor":null`) {
		t.Fatal(w.Body.String())
	}
	w = ledgerRequest(func(context.Context, string, ...any) (pgx.Rows, error) {
		return &ledgerRows{err: errors.New("private database detail")}, nil
	}, "balance", "", 7)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private database detail") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestEntitlementLedgerPreservesPartialDays(t *testing.T) {
	w := ledgerRequest(func(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
		if !strings.Contains(sql, "subscription_ledger") {
			t.Fatal(sql)
		}
		return &ledgerRows{values: [][]any{{int64(4), "ADMIN_ADJUST", int64(-3600), int64(0), int64(0), "调整", time.Now()}}}, nil
	}, "entitlements", "", 7)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"seconds_delta":-3600`) || !strings.Contains(w.Body.String(), `"next_cursor":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
