package outbox

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type outboxFixture struct {
	ctx         context.Context
	pool, owner *pgxpool.Pool
	schema      string
	now         time.Time
}

// Never claim events from the application schema, even when a developer shares
// a test database with other test packages. Only synthetic events live here.
func newOutboxFixture(t *testing.T) *outboxFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL for isolated-schema tests")
	}
	app, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid application test configuration")
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid owner test configuration")
	}
	if app.ConnConfig.Host != ownerConfig.ConnConfig.Host || app.ConnConfig.Port != ownerConfig.ConnConfig.Port || app.ConnConfig.Database != ownerConfig.ConnConfig.Database {
		t.Fatal("application and owner test databases must match")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	schema := "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := owner.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("outbox fixture cleanup: %v", err)
		}
	})
	role := pgx.Identifier{app.ConnConfig.User}.Sanitize()
	for _, sql := range []string{
		"CREATE TABLE " + quoted + ".outbox_events (LIKE public.outbox_events INCLUDING ALL)",
		"CREATE TABLE " + quoted + ".effects (event_id bigint PRIMARY KEY)",
		"GRANT USAGE ON SCHEMA " + quoted + " TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + quoted + " TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	// No public fallback: a typo cannot accidentally reach a real event table.
	app.ConnConfig.RuntimeParams["search_path"] = schema
	app.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel() // Unblock test handlers before waiting for pool connections.
		pool.Close()
	})
	return &outboxFixture{ctx: ctx, pool: pool, owner: owner, schema: schema, now: time.Date(2035, 4, 5, 6, 7, 8, 0, time.UTC)}
}

func (f *outboxFixture) add(t *testing.T, eventType EventType, aggregate int64, due time.Time, status string) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload,next_retry_at,status) VALUES($1,'fixture',$2,'{}',$3,$4) RETURNING id`, eventType, aggregate, due, status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type eventState struct {
	status   string
	attempts int
	next     time.Time
	err      *string
}

func (f *outboxFixture) state(t *testing.T, id int64) eventState {
	t.Helper()
	var s eventState
	if err := f.pool.QueryRow(f.ctx, `SELECT status,attempts,next_retry_at,last_error FROM outbox_events WHERE id=$1`, id).Scan(&s.status, &s.attempts, &s.next, &s.err); err != nil {
		t.Fatal(err)
	}
	return s
}

func runResult(t *testing.T, ctx context.Context, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("dispatcher did not finish", ctx.Err())
	}
}

func reachedHandler(t *testing.T, ctx context.Context, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("dispatcher did not enter handler", ctx.Err())
	}
}

func TestDispatcherBusinessTimeUnknownAndTerminalIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	ready := f.add(t, OrderCompleted, 1, f.now, "PENDING")
	future := f.add(t, OrderCompleted, 2, f.now.Add(time.Hour), "PENDING")
	unknown := f.add(t, "FUTURE_EVENT", 3, f.now, "PENDING")
	done := f.add(t, OrderCompleted, 4, f.now, "DONE")
	dead := f.add(t, OrderCompleted, 5, f.now, "DEAD")
	calls := 0
	d := &Dispatcher{Pool: f.pool, Lease: time.Minute, Handlers: map[EventType]Handler{
		OrderCompleted: func(context.Context, Event) error { calls++; return nil },
	}}
	if err := d.Run(f.ctx, f.now); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, ready); s.status != "DONE" || s.attempts != 1 || !s.next.Equal(f.now.Add(time.Minute)) || calls != 1 {
		t.Fatal("business time was not used for claim", s, calls)
	}
	if s := f.state(t, future); s.status != "PENDING" || s.attempts != 0 {
		t.Fatal("future event was claimed early", s)
	}
	if err := d.Run(f.ctx, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || f.state(t, future).status != "DONE" || f.state(t, ready).attempts != 1 {
		t.Fatal("a completed event was replayed", calls)
	}
	for _, id := range []int64{unknown, done, dead} {
		if s := f.state(t, id); s.attempts != 0 {
			t.Fatal("unregistered or terminal event claimed", id, s)
		}
	}
}

func TestDispatcherStaleAttemptCannotOverwriteNewClaimIntegration(t *testing.T) {
	for _, oldFails := range []bool{false, true} {
		for _, newerFinishesFirst := range []bool{false, true} {
			name := "old_success"
			if oldFails {
				name = "old_failure"
			}
			if newerFinishesFirst {
				name += "_after_new_done"
			} else {
				name += "_while_new_pending"
			}
			t.Run(name, func(t *testing.T) {
				f := newOutboxFixture(t)
				id := f.add(t, OrderCompleted, 1, f.now, "PENDING")
				entered, release := make(chan struct{}), make(chan struct{})
				oldResult := make(chan error, 1)
				old := &Dispatcher{Pool: f.pool, Lease: time.Second, MaxAttempts: 1, Handlers: map[EventType]Handler{
					OrderCompleted: func(ctx context.Context, ev Event) error {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return ctx.Err()
						}
						if oldFails {
							return errors.New("stale failure")
						}
						return nil
					},
				}}
				go func() { oldResult <- old.Run(f.ctx, f.now) }()
				reachedHandler(t, f.ctx, entered)
				newEntered, newRelease := make(chan struct{}), make(chan struct{})
				newResult := make(chan error, 1)
				newer := &Dispatcher{Pool: f.pool, Lease: time.Second, Handlers: map[EventType]Handler{
					OrderCompleted: func(ctx context.Context, ev Event) error {
						close(newEntered)
						select {
						case <-newRelease:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					},
				}}
				go func() { newResult <- newer.Run(f.ctx, f.now.Add(time.Second)) }()
				reachedHandler(t, f.ctx, newEntered)
				if newerFinishesFirst {
					close(newRelease)
					runResult(t, f.ctx, newResult)
				}
				before := f.state(t, id)
				close(release)
				runResult(t, f.ctx, oldResult)
				after := f.state(t, id)
				if after.attempts != 2 || after.status != before.status || !after.next.Equal(before.next) || after.err != nil {
					t.Fatal("stale attempt overwrote new claim", before, after)
				}
				if !newerFinishesFirst {
					close(newRelease)
					runResult(t, f.ctx, newResult)
				}
				if f.state(t, id).status != "DONE" {
					t.Fatal("new attempt did not finish")
				}
			})
		}
	}
}

func TestDispatcherCannotOverwriteTerminalStateOfSameAttemptIntegration(t *testing.T) {
	for _, terminal := range []string{"DONE", "DEAD"} {
		t.Run(terminal, func(t *testing.T) {
			f := newOutboxFixture(t)
			id := f.add(t, OrderCompleted, 1, f.now, "PENDING")
			d := &Dispatcher{Pool: f.pool, MaxAttempts: 1, Handlers: map[EventType]Handler{
				OrderCompleted: func(ctx context.Context, ev Event) error {
					if _, err := f.pool.Exec(ctx, `UPDATE outbox_events SET status=$2 WHERE id=$1`, ev.ID, terminal); err != nil {
						return err
					}
					if terminal == "DONE" {
						return errors.New("late failure")
					}
					return nil
				},
			}}
			if err := d.Run(f.ctx, f.now); err != nil {
				t.Fatal(err)
			}
			if s := f.state(t, id); s.status != terminal || s.err != nil {
				t.Fatal("terminal event overwritten", s)
			}
		})
	}
}

func TestDispatcherRetryBackoffAndDeadIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	id := f.add(t, OrderCompleted, 1, f.now, "PENDING")
	calls := 0
	d := &Dispatcher{Pool: f.pool, MaxAttempts: 2, Handlers: map[EventType]Handler{
		OrderCompleted: func(context.Context, Event) error { calls++; return errors.New("SECRET_CANARY_do_not_persist") },
	}}
	if err := d.Run(f.ctx, f.now); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, id); s.status != "PENDING" || s.attempts != 1 || s.err == nil || *s.err != "handler_failed" || !s.next.Equal(f.now.Add(time.Minute)) {
		t.Fatal("incorrect retry state", s)
	}
	if err := d.Run(f.ctx, f.now.Add(59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("backoff ignored", calls)
	}
	if err := d.Run(f.ctx, f.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := f.state(t, id); s.status != "DEAD" || s.attempts != 2 || !s.next.Equal(f.now.Add(3*time.Minute)) {
		t.Fatal("attempt limit ignored", s)
	}
	if err := d.Run(f.ctx, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("dead event replayed", calls)
	}
}

func TestDispatcherPersistenceFailureAndIdempotentReplayIntegration(t *testing.T) {
	for _, handlerFails := range []bool{false, true} {
		name := "success_ack"
		if handlerFails {
			name = "failure_ack"
		}
		t.Run(name, func(t *testing.T) {
			f := newOutboxFixture(t)
			id := f.add(t, OrderCompleted, 1, f.now, "PENDING")
			q := pgx.Identifier{f.schema}.Sanitize()
			for _, sql := range []string{
				"CREATE FUNCTION " + q + `.reject_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.attempts=OLD.attempts THEN RAISE EXCEPTION 'outbox persist denied' USING ERRCODE='P0001'; END IF; RETURN NEW; END $$`,
				"CREATE TRIGGER reject_ack BEFORE UPDATE ON " + q + ".outbox_events FOR EACH ROW EXECUTE FUNCTION " + q + ".reject_ack()",
			} {
				if _, err := f.owner.Exec(f.ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			d := &Dispatcher{Pool: f.pool, Lease: time.Second, Handlers: map[EventType]Handler{
				OrderCompleted: func(ctx context.Context, ev Event) error {
					calls++
					if _, err := f.pool.Exec(ctx, `INSERT INTO effects(event_id) VALUES($1) ON CONFLICT DO NOTHING`, ev.ID); err != nil {
						return err
					}
					if handlerFails && calls == 1 {
						return errors.New("handler interrupted after durable effect")
					}
					return nil
				},
			}}
			err := d.Run(f.ctx, f.now)
			var dbErr *pgconn.PgError
			if !errors.As(err, &dbErr) || dbErr.Code != "P0001" {
				t.Fatal("persistence failure hidden", err)
			}
			if s := f.state(t, id); s.status != "PENDING" || s.attempts != 1 || s.err != nil {
				t.Fatal("failed write was acknowledged", s)
			}
			if _, err := f.owner.Exec(f.ctx, "DROP TRIGGER reject_ack ON "+q+".outbox_events"); err != nil {
				t.Fatal(err)
			}
			if err := d.Run(f.ctx, f.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var effects int
			if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM effects`).Scan(&effects); err != nil {
				t.Fatal(err)
			}
			if effects != 1 || calls != 2 || f.state(t, id).status != "DONE" {
				t.Fatal("replay duplicated effect or failed to finish", effects, calls)
			}
		})
	}
}

func TestDispatcherCancellationLeavesClaimedEventsRecoverableIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	first := f.add(t, OrderCompleted, 1, f.now, "PENDING")
	second := f.add(t, OrderCompleted, 2, f.now, "PENDING")
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	calls := 0
	d := &Dispatcher{Pool: f.pool, Batch: 2, Lease: time.Second, Handlers: map[EventType]Handler{
		OrderCompleted: func(context.Context, Event) error { calls++; cancel(); return nil },
	}}
	if err := d.Run(ctx, f.now); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation hidden", err)
	}
	if calls != 1 {
		t.Fatal("handlers continued after cancellation", calls)
	}
	for _, id := range []int64{first, second} {
		if s := f.state(t, id); s.status != "PENDING" || s.attempts != 1 {
			t.Fatal("cancelled batch was acknowledged", s)
		}
	}
	d.Handlers[OrderCompleted] = func(context.Context, Event) error { return nil }
	if err := d.Run(f.ctx, f.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first, second} {
		if s := f.state(t, id); s.status != "DONE" || s.attempts != 2 {
			t.Fatal("cancelled event could not recover", s)
		}
	}
}

func TestDispatcherClaimErrorDoesNotReachHandlerIntegration(t *testing.T) {
	f := newOutboxFixture(t)
	id := f.add(t, OrderCompleted, 1, f.now, "PENDING")
	q := pgx.Identifier{f.schema, "outbox_events"}.Sanitize()
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+q+" ADD CONSTRAINT reject_claim CHECK (attempts = 0)"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	d := &Dispatcher{Pool: f.pool, Handlers: map[EventType]Handler{
		OrderCompleted: func(context.Context, Event) error { calls++; return nil },
	}}
	err := d.Run(f.ctx, f.now)
	var dbErr *pgconn.PgError
	if !errors.As(err, &dbErr) || dbErr.Code != "23514" || calls != 0 {
		t.Fatal("failed claim reached handler", err, calls)
	}
	if s := f.state(t, id); s.attempts != 0 || s.status != "PENDING" {
		t.Fatal("failed claim changed event", s)
	}
}
