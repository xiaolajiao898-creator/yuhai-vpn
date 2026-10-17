package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type auditClock struct{ now time.Time }

func (c auditClock) Now() time.Time { return c.now }

type auditFixture struct {
	ctx           context.Context
	pool, owner   *pgxpool.Pool
	svc           *Service
	schema, table string
}

func newAuditFixture(t *testing.T) *auditFixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	a, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid app test database configuration")
	}
	o, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid owner test database configuration")
	}
	if a.ConnConfig.Host != o.ConnConfig.Host || a.ConnConfig.Port != o.ConnConfig.Port || a.ConnConfig.Database != o.ConnConfig.Database {
		t.Fatal("test databases must match")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.NewWithConfig(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	schema := "audit_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quoted := pgx.Identifier{schema}.Sanitize()
	table := pgx.Identifier{schema, "admin_audit_logs"}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := owner.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	})
	role := pgx.Identifier{a.ConnConfig.User}.Sanitize()
	for _, q := range []string{
		"CREATE TABLE " + table + " (LIKE public.admin_audit_logs INCLUDING ALL)",
		"CREATE TRIGGER trg_admin_audit_logs_append_only BEFORE UPDATE OR DELETE ON " + table + " FOR EACH ROW EXECUTE FUNCTION public.forbid_mutation()",
		"CREATE TABLE " + pgx.Identifier{schema, "business_change"}.Sanitize() + " (id int PRIMARY KEY,value int NOT NULL)",
		"INSERT INTO " + pgx.Identifier{schema, "business_change"}.Sanitize() + " VALUES(1,0)",
		"GRANT USAGE ON SCHEMA " + quoted + " TO " + role,
		"GRANT SELECT,INSERT ON ALL TABLES IN SCHEMA " + quoted + " TO " + role,
		"GRANT UPDATE ON " + pgx.Identifier{schema, "business_change"}.Sanitize() + " TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role,
	} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	a.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	a.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	clk := auditClock{time.Date(2035, 1, 2, 11, 4, 5, 123456789, time.FixedZone("UTC+8", 8*60*60))}
	return &auditFixture{ctx: ctx, pool: pool, owner: owner, schema: schema, table: table, svc: &Service{Pool: pool, Clock: clk}}
}

func (f *auditFixture) event() Event {
	admin, target := int64(7), int64(9)
	return Event{AdminUserID: &admin, ActionCode: "app.version.publish", TargetType: "app_version", TargetID: &target, RequestID: strings.ReplaceAll(uuid.NewString(), "-", ""), IP: "2001:db8::1", UserAgent: "fixture agent", Result: "SUCCESS", StepUpVerified: true, BeforeData: json.RawMessage(`{"status":"DRAFT"}`), AfterData: json.RawMessage(`{"status":"PUBLISHED","version_code":"9007199254740993"}`)}
}

func (f *auditFixture) append(e Event) error {
	return db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error { return f.svc.Append(f.ctx, tx, e) })
}
func (f *auditFixture) ids(t *testing.T) []int64 {
	t.Helper()
	rows, err := f.pool.Query(f.ctx, `SELECT id FROM admin_audit_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}
func (f *auditFixture) mutate(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+f.table+" DISABLE TRIGGER trg_admin_audit_logs_append_only"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+f.table+" ENABLE TRIGGER trg_admin_audit_logs_append_only"); err != nil {
			t.Error(err)
		}
	}()
	if _, err := f.owner.Exec(f.ctx, fmt.Sprintf(q, f.table), args...); err != nil {
		t.Fatal(err)
	}
}
func (f *auditFixture) assertBusiness(t *testing.T, wantValue, wantRows int) {
	t.Helper()
	var value, rows int
	if err := f.pool.QueryRow(f.ctx, `SELECT value,(SELECT count(*) FROM admin_audit_logs) FROM business_change WHERE id=1`).Scan(&value, &rows); err != nil || value != wantValue || rows != wantRows {
		t.Fatalf("business/audit atomicity: value=%d rows=%d err=%v", value, rows, err)
	}
}

func TestAuditConcurrentAppendAndMetadataOnlyListIntegration(t *testing.T) {
	f := newAuditFixture(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// No common business-row lock: only the audit lock can serialize
			// these independent transactions into one chain.
			errs <- f.append(f.event())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.assertBusiness(t, 0, n)
	ids := f.ids(t)
	result, err := f.svc.Verify(f.ctx, ids[0], ids[len(ids)-1])
	if err != nil || !result.Valid || result.Checked != n || result.ExternalAnchor || result.FirstBrokenID != nil {
		t.Fatal(result, err)
	}
	page, err := f.svc.List(f.ctx, 0, 0, "", "")
	if err != nil || page.Page != 1 || page.PageSize != 20 || page.Total != "16" || len(page.Items) != n {
		t.Fatal(page, err)
	}
	for i, entry := range page.Items {
		if entry.ID != strconv.FormatInt(ids[len(ids)-1-i], 10) {
			t.Fatalf("list must sort BIGINT identities numerically, not text aliases: index=%d id=%s", i, entry.ID)
		}
	}
	if page.Items[0].CreatedAt != "2035-01-02T03:04:05.123456Z" {
		t.Fatal("UTC microsecond timestamp lost", page.Items[0].CreatedAt)
	}
	b, _ := json.Marshal(page)
	for _, forbidden := range []string{"before_data", "after_data", "2001:db8", "fixture agent"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatal("list leaked private snapshot or request metadata", forbidden)
		}
	}
	second, err := f.svc.List(f.ctx, 2, 5, "app.version.publish", "SUCCESS")
	if err != nil || second.Total != "16" || len(second.Items) != 5 || second.Items[0].ID != strconv.FormatInt(ids[10], 10) {
		t.Fatal(second, err)
	}
	empty, err := f.svc.List(f.ctx, 1, 20, "app.version.publish", "DENIED")
	if err != nil || empty.Total != "0" || len(empty.Items) != 0 {
		t.Fatal(empty, err)
	}
}

func TestAuditRollbackAndSequenceGapsIntegration(t *testing.T) {
	f := newAuditFixture(t)
	sentinel := errors.New("later business failure")
	err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.ctx, `UPDATE business_change SET value=1 WHERE id=1`); err != nil {
			return err
		}
		if err := f.svc.Append(f.ctx, tx, f.event()); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	f.assertBusiness(t, 0, 0)
	if err = f.append(f.event()); err != nil {
		t.Fatal(err)
	}
	ids := f.ids(t)
	if len(ids) != 1 || ids[0] <= 1 {
		t.Fatal("fixture failed to consume rollback sequence", ids)
	}
	r, err := f.svc.Verify(f.ctx, ids[0], ids[0])
	if err != nil || !r.Valid || r.Checked != 1 {
		t.Fatal("valid genesis after rolled-back identity rejected", r, err)
	}
	if _, err = f.owner.Exec(f.ctx, "UPDATE "+f.table+" SET result='FAILED'"); err == nil {
		t.Fatal("append-only trigger allowed update")
	}
	if _, err = f.owner.Exec(f.ctx, "DELETE FROM "+f.table); err == nil {
		t.Fatal("append-only trigger allowed delete")
	}
	if err = f.append(f.event()); err != nil {
		t.Fatal(err)
	}
	ids = f.ids(t)
	r, err = f.svc.Verify(f.ctx, ids[0], ids[1])
	if err != nil || !r.Valid {
		t.Fatal(r, err)
	}
}

func TestAuditFailureRollsBackBusinessAndRejectsStaleIsolationIntegration(t *testing.T) {
	f := newAuditFixture(t)
	if _, err := f.owner.Exec(f.ctx, "ALTER TABLE "+f.table+" ADD CONSTRAINT reject_fixture_audit CHECK(action_code <> 'app.version.publish')"); err != nil {
		t.Fatal(err)
	}
	err := db.InTx(f.ctx, f.pool, func(tx pgx.Tx) error {
		if _, e := tx.Exec(f.ctx, `UPDATE business_change SET value=1 WHERE id=1`); e != nil {
			return e
		}
		return f.svc.Append(f.ctx, tx, f.event())
	})
	if err == nil {
		t.Fatal("injected audit failure swallowed")
	}
	f.assertBusiness(t, 0, 0)
	tx, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Append(f.ctx, tx, f.event()); err == nil {
		t.Fatal("snapshot transaction could fork audit chain")
	}
	if err = tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.assertBusiness(t, 0, 0)
}

func TestAuditTamperPredecessorMissingAndBoundaryIntegration(t *testing.T) {
	for _, tc := range []struct {
		name, sql, reason string
		from, to, broken  int
	}{
		{"row_data", `UPDATE %s SET after_data='{"status":"FAILED"}' WHERE id=$1`, "ROW_HASH_MISMATCH", 0, 2, 0},
		{"predecessor_data", `UPDATE %s SET result='FAILED' WHERE id=$1`, "PREDECESSOR_HASH_MISMATCH", 1, 2, 0},
		{"missing_middle", `DELETE FROM %s WHERE id=$1`, "PREVIOUS_HASH_MISMATCH", 0, 2, 2},
		{"missing_from", `DELETE FROM %s WHERE id=$1`, "MISSING_FROM", 0, 2, 0},
		{"missing_to", `DELETE FROM %s WHERE id=$1`, "MISSING_TO", 0, 2, 2},
		{"genesis_link", `UPDATE %s SET prev_hash=repeat('1',64) WHERE id=$1`, "PREVIOUS_HASH_MISMATCH", 0, 2, 0},
		{"json_numeric", `UPDATE %s SET after_data='{"version_code":9007199254740993}' WHERE id=$1`, "ROW_HASH_MISMATCH", 0, 2, 0},
		{"inet_mask", `UPDATE %s SET ip='2001:db8::1/64' WHERE id=$1`, "ROW_HASH_MISMATCH", 0, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuditFixture(t)
			for i := 0; i < 3; i++ {
				if err := f.append(f.event()); err != nil {
					t.Fatal(err)
				}
			}
			ids := f.ids(t)
			change := ids[0]
			if tc.name == "missing_middle" {
				change = ids[1]
			}
			if tc.name == "missing_to" {
				change = ids[2]
			}
			f.mutate(t, tc.sql, change)
			r, err := f.svc.Verify(f.ctx, ids[tc.from], ids[tc.to])
			if err != nil || r.Valid || r.Reason != tc.reason || r.FirstBrokenID == nil || *r.FirstBrokenID != strconv.FormatInt(ids[tc.broken], 10) {
				t.Fatal(r, err)
			}
		})
	}
	// A correctly rehashed predecessor with a broken link still fails at the
	// predecessor boundary; verifying just its own bytes would miss this.
	t.Run("predecessor_rehashed_bad_link", func(t *testing.T) {
		f := newAuditFixture(t)
		for i := 0; i < 3; i++ {
			if err := f.append(f.event()); err != nil {
				t.Fatal(err)
			}
		}
		ids := f.ids(t)
		r, err := scanRecord(f.pool.QueryRow(f.ctx, `SELECT `+recordColumns+` FROM admin_audit_logs WHERE id=$1`, ids[1]))
		if err != nil {
			t.Fatal(err)
		}
		r.PrevHash = zeroHash()
		b, _, _ := snapshot(r.BeforeData)
		a, _, _ := snapshot(r.AfterData)
		hash, err := hashRecord(r, b, a)
		if err != nil {
			t.Fatal(err)
		}
		f.mutate(t, `UPDATE %s SET prev_hash=$2,row_hash=$3 WHERE id=$1`, ids[1], r.PrevHash, hash)
		got, err := f.svc.Verify(f.ctx, ids[2], ids[2])
		if err != nil || got.Valid || got.Reason != "PREDECESSOR_LINK_MISMATCH" {
			t.Fatal(got, err)
		}
		if err = f.append(f.event()); !errors.Is(err, ErrChain) {
			t.Fatal("append extended known corrupt head", err)
		}
	})
}

func TestAuditVerifyEmptyOversizeAndConcurrentAppendIntegration(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		f := newAuditFixture(t)
		r, err := f.svc.Verify(f.ctx, 1, 1)
		if err != nil || r.Valid || r.HeadID != "0" || r.Reason != "MISSING_FROM" {
			t.Fatal(r, err)
		}
	})
	t.Run("maximum_rows", func(t *testing.T) {
		f := newAuditFixture(t)
		_, err := f.owner.Exec(f.ctx, "INSERT INTO "+f.table+" (id,action_code,request_id,result,prev_hash,row_hash) OVERRIDING SYSTEM VALUE SELECT n,'fixture.bulk','req','SUCCESS',repeat('0',64),repeat('0',64) FROM generate_series(1,10001) AS n")
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.svc.Verify(f.ctx, 1, 10001)
		var business *httpx.Error
		if !errors.As(err, &business) || business.Code != httpx.ErrBadRequest {
			t.Fatal("oversized verification was not refused", err)
		}
	})
	t.Run("bounded_range_during_append", func(t *testing.T) {
		f := newAuditFixture(t)
		for i := 0; i < 3; i++ {
			if err := f.append(f.event()); err != nil {
				t.Fatal(err)
			}
		}
		ids := f.ids(t)
		done := make(chan error, 1)
		go func() {
			for i := 0; i < 16; i++ {
				if err := f.append(f.event()); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
		for i := 0; i < 8; i++ {
			r, err := f.svc.Verify(f.ctx, ids[0], ids[2])
			if err != nil || !r.Valid || r.Checked != 3 || r.ToID != strconv.FormatInt(ids[2], 10) {
				t.Fatal(r, err)
			}
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
