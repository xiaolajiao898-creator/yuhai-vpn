package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDispatcherRejectsInvalidOptionsAndCancelledContext(t *testing.T) {
	for _, d := range []Dispatcher{
		{Batch: -1}, {Batch: 1001}, {Lease: -time.Second},
		{Lease: time.Millisecond}, {Lease: time.Hour + time.Second},
		{MaxAttempts: -1}, {MaxAttempts: 1001},
	} {
		if err := d.Run(context.Background(), time.Time{}); err == nil {
			t.Fatalf("invalid options accepted: %+v", d)
		}
	}
	d := &Dispatcher{}
	batch, lease, maxAttempts, err := d.options()
	if err != nil || batch != 50 || lease != 5*time.Minute || maxAttempts != 20 {
		t.Fatal("incorrect defaults", batch, lease, maxAttempts, err)
	}
	if err := d.Run(context.Background(), time.Time{}); err != nil {
		t.Fatal("an unconfigured dispatcher should leave all types queued", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Run(ctx, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context was ignored", err)
	}
	d.Handlers = map[EventType]Handler{OrderCompleted: func(context.Context, Event) error { return nil }}
	if err := d.Run(context.Background(), time.Time{}); err == nil {
		t.Fatal("active dispatcher requires a database and business time")
	}
}

// A claim can yield rows before the server reports a statement error. Partial
// data must never reach handlers when the statement itself failed.
func TestReadEventsRejectsDeferredAndScanErrors(t *testing.T) {
	sentinel := errors.New("claim stream interrupted")
	for _, scanError := range []bool{false, true} {
		rows := &brokenRows{err: sentinel, scanError: scanError}
		events, err := readEvents(rows)
		if !errors.Is(err, sentinel) || len(events) != 0 || !rows.closed {
			t.Fatal("partial claim survived its error", events, err, rows.closed)
		}
	}
}

type brokenRows struct {
	pgx.Rows
	err                error
	scanError, yielded bool
	closed             bool
}

func (r *brokenRows) Next() bool {
	if r.yielded {
		return false
	}
	r.yielded = true
	return true
}
func (r *brokenRows) Scan(dest ...any) error {
	if r.scanError {
		return r.err
	}
	*dest[0].(*int64) = 1
	*dest[1].(*string) = string(OrderCompleted)
	*dest[2].(*string) = "order"
	*dest[3].(*int64) = 1
	*dest[4].(*[]byte) = []byte(`{}`)
	*dest[5].(*int) = 1
	return nil
}
func (r *brokenRows) Close()     { r.closed = true }
func (r *brokenRows) Err() error { return r.err }
