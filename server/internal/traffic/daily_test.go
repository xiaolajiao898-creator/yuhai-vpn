package traffic

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type queryFunc func(context.Context, string, ...any) (pgx.Rows, error)

func (q queryFunc) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return q(ctx, sql, args...)
}

type testRows struct {
	pgx.Rows
	values  [][]any
	pos     int
	err     error
	scanErr error
	closed  bool
}

func (r *testRows) Next() bool { r.pos++; return r.pos <= len(r.values) }
func (r *testRows) Close()     { r.closed = true }
func (r *testRows) Err() error { return r.err }
func (r *testRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	for i, value := range r.values[r.pos-1] {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func TestDailyTrafficUTCWindowIncludesTodayAndLeapDay(t *testing.T) {
	// Local March 2 is still March 1 in UTC; window must include February 29.
	now := time.Date(2028, 3, 2, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	rows := &testRows{values: [][]any{{"2028-02-29", int64(3), "10", "20", false}, {"2028-02-29", int64(5), "7", "3", false}}}
	svc := &Service{Clock: fixedClock{now}, Pool: queryFunc(func(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
		if !reflect.DeepEqual(args, []any{int64(7), "2028-02-28", "2028-03-02"}) {
			t.Fatalf("wrong UTC interval or identity: %v", args)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Fatal("query missing bounded deadline")
		}
		if !strings.Contains(sql, "t.user_id=$1") || !strings.Contains(sql, "d.user_id=t.user_id") || strings.Contains(sql, "d.status") {
			t.Fatal("query must scope historical ownership without filtering revoked devices")
		}
		return rows, nil
	})}
	result, err := svc.Daily(context.Background(), 7, 3)
	want := DailyUsage{Days: []Day{
		{Date: "2028-02-28", ByDevice: []DeviceUsage{}},
		{Date: "2028-02-29", UpBytes: 17, DownBytes: 23, ByDevice: []DeviceUsage{{DeviceID: "3", Bytes: 30}, {DeviceID: "5", Bytes: 10}}},
		{Date: "2028-03-01", ByDevice: []DeviceUsage{}},
	}}
	if err != nil || !reflect.DeepEqual(result, want) || !rows.closed {
		t.Fatal(result, err, rows.closed)
	}
}

func TestDailyTrafficValidationAndCancellation(t *testing.T) {
	// Invalid input must fail before touching the clock or database.
	svc := &Service{}
	for _, input := range []struct {
		user int64
		days int
	}{{0, 30}, {-1, 30}, {1, 0}, {1, -1}, {1, 91}} {
		if _, err := svc.Daily(context.Background(), input.user, input.days); err == nil {
			t.Fatal("accepted invalid service input", input)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.Clock = fixedClock{time.Now()}
	svc.Pool = queryFunc(func(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) { return nil, ctx.Err() })
	if _, err := svc.Daily(ctx, 1, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("query lost cancellation", err)
	}
}

func TestDailyTrafficRejectsUnsafeAggregatesWithoutPartialResponse(t *testing.T) {
	max := strconv.FormatInt(maxSafeBytes, 10)
	for _, tc := range []struct {
		name string
		rows *testRows
	}{
		{"numeric larger than int64", &testRows{values: [][]any{{"2026-09-12", int64(1), "18446744073709551614", "0", false}}}},
		{"larger than JSON safe integer", &testRows{values: [][]any{{"2026-09-12", int64(1), "9007199254740992", "0", false}}}},
		{"negative row cancelled by positive", &testRows{values: [][]any{{"2026-09-12", int64(1), "1", "0", true}}}},
		{"negative aggregate", &testRows{values: [][]any{{"2026-09-12", int64(1), "-1", "0", false}}}},
		{"device sum overflow", &testRows{values: [][]any{{"2026-09-12", int64(1), max, "1", false}}}},
		{"day sum overflow", &testRows{values: [][]any{{"2026-09-12", int64(1), max, "0", false}, {"2026-09-12", int64(2), "1", "0", false}}}},
		{"period sum overflow", &testRows{values: [][]any{{"2026-09-11", int64(1), max, "0", false}, {"2026-09-12", int64(1), "1", "0", false}}}},
		{"stream error", &testRows{err: errors.New("interrupted query")}},
		{"scan error", &testRows{values: [][]any{{}}, scanErr: errors.New("invalid database type")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{Clock: fixedClock{time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}, Pool: queryFunc(func(context.Context, string, ...any) (pgx.Rows, error) { return tc.rows, nil })}
			result, err := svc.Daily(context.Background(), 1, 2)
			if err == nil || result.Days != nil || !tc.rows.closed {
				t.Fatal("returned incomplete or unsafe aggregate", result, err, tc.rows.closed)
			}
		})
	}
	if n, err := safeBytes(max); err != nil || n != maxSafeBytes {
		t.Fatal("safe upper bound rejected", n, err)
	}
}
