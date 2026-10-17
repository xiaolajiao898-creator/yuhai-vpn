package traffic

import (
	"context"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/ids"
)

// Only TEST_* connections are used. Owner privileges create two temporary
// test partitions; app-role fixtures and all service reads use a rollback-only
// transaction. Cleanup drops only the uniquely named partitions made here.
func newTrafficIntegrationFixture(t *testing.T) (context.Context, pgx.Tx, *Service) {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_MIGRATE_DATABASE_URL to an isolated migrated database")
	}
	appConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test application database configuration")
	}
	ownerConfig, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid test owner database configuration")
	}
	if appConfig.ConnConfig.Host != ownerConfig.ConnConfig.Host || appConfig.ConnConfig.Port != ownerConfig.ConnConfig.Port || appConfig.ConnConfig.Database != ownerConfig.ConnConfig.Database {
		t.Fatal("test application and owner must use the same isolated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	app, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	names := []string{"traffic_daily_test_dec_" + suffix, "traffic_daily_test_jan_" + suffix}
	partitionTx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer partitionTx.Rollback(context.Background())
	for i, bounds := range [][2]string{{"2088-12-01", "2089-01-01"}, {"2089-01-01", "2089-02-01"}} {
		ddl := fmt.Sprintf(`CREATE TABLE %s PARTITION OF traffic_daily FOR VALUES FROM ('%s') TO ('%s')`, pgx.Identifier{names[i]}.Sanitize(), bounds[0], bounds[1])
		if _, err := partitionTx.Exec(ctx, ddl); err != nil {
			t.Fatal("create isolated traffic partition fixture", err)
		}
	}
	if err := partitionTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		for _, name := range names {
			if _, err := owner.Exec(cleanupCtx, "DROP TABLE "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Errorf("drop owned fixture partition: %v", err)
			}
		}
	})
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	// DATE serialization must not depend on the database session's DateStyle.
	if _, err := tx.Exec(ctx, `SET LOCAL DateStyle TO 'SQL, DMY'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2089, 1, 2, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	return ctx, tx, &Service{Pool: tx, Clock: fixedClock{now}}
}

func newTrafficUser(t *testing.T, ctx context.Context, tx pgx.Tx) int64 {
	t.Helper()
	accountNo, err := ids.NewAccountNo()
	if err != nil {
		t.Fatal(err)
	}
	code, err := ids.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO users(account_no,secret_lookup,invite_code) VALUES($1,$2,$3) RETURNING id`, accountNo, uuid.NewString(), code).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTrafficDevice(t *testing.T, ctx context.Context, tx pgx.Tx, userID int64, status string) int64 {
	t.Helper()
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO devices(user_id,device_uid,pubkey,platform,status) VALUES($1,$2,'traffic-test','ANDROID',$3) RETURNING id`, userID, uuid.NewString(), status).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func addTraffic(t *testing.T, ctx context.Context, tx pgx.Tx, date string, userID, deviceID, nodeID, up, down int64) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO traffic_daily(stat_date,user_id,device_id,node_id,upload_bytes,download_bytes) VALUES($1::date,$2,$3,$4,$5,$6)`, date, userID, deviceID, nodeID, up, down); err != nil {
		t.Fatal(err)
	}
}

func TestDailyTrafficPartitionedHistoryIsolationAndBoundsIntegration(t *testing.T) {
	ctx, tx, svc := newTrafficIntegrationFixture(t)
	a, b, emptyUser := newTrafficUser(t, ctx, tx), newTrafficUser(t, ctx, tx), newTrafficUser(t, ctx, tx)
	a1 := newTrafficDevice(t, ctx, tx, a, "ACTIVE")
	a2 := newTrafficDevice(t, ctx, tx, a, "REVOKED")
	b1 := newTrafficDevice(t, ctx, tx, b, "ACTIVE")
	addTraffic(t, ctx, tx, "2088-12-29", a, a1, 1, 100, 100) // Before a three-day window.
	addTraffic(t, ctx, tx, "2088-12-30", a, a1, 1, 10, 20)
	addTraffic(t, ctx, tx, "2088-12-30", a, a1, 2, 3, 7) // Same device, second node.
	addTraffic(t, ctx, tx, "2088-12-30", a, a2, 1, 5, 15)
	addTraffic(t, ctx, tx, "2089-01-01", a, a2, 2, 2, 8)     // Revoked device history in next partition.
	addTraffic(t, ctx, tx, "2089-01-02", a, a1, 1, 100, 100) // Local today is UTC tomorrow.
	addTraffic(t, ctx, tx, "2088-12-30", b, b1, 1, 999, 999)
	addTraffic(t, ctx, tx, "2088-12-30", a, b1, 2, 1234, 1234) // Malformed foreign-device attribution.
	result, err := svc.Daily(ctx, a, 3)
	want := DailyUsage{Days: []Day{
		{Date: "2088-12-30", UpBytes: 18, DownBytes: 42, ByDevice: []DeviceUsage{{DeviceID: strconv.FormatInt(a1, 10), Bytes: 40}, {DeviceID: strconv.FormatInt(a2, 10), Bytes: 20}}},
		{Date: "2088-12-31", ByDevice: []DeviceUsage{}},
		{Date: "2089-01-01", UpBytes: 2, DownBytes: 8, ByDevice: []DeviceUsage{{DeviceID: strconv.FormatInt(a2, 10), Bytes: 10}}},
	}}
	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("wrong partitioned aggregate: %#v err=%v", result, err)
	}
	other, err := svc.Daily(ctx, b, 3)
	if err != nil || other.Days[0].UpBytes != 999 || other.Days[0].DownBytes != 999 || len(other.Days[0].ByDevice) != 1 || other.Days[0].ByDevice[0].DeviceID != strconv.FormatInt(b1, 10) {
		t.Fatal("cross-account traffic leak", other, err)
	}
	for _, days := range []int{1, defaultDays, maxDays} {
		empty, err := svc.Daily(ctx, emptyUser, days)
		if err != nil || len(empty.Days) != days || empty.Days[days-1].Date != "2089-01-01" {
			t.Fatal("wrong zero-filled range", days, empty, err)
		}
		for i, day := range empty.Days {
			if day.UpBytes != 0 || day.DownBytes != 0 || day.ByDevice == nil || len(day.ByDevice) != 0 || (i > 0 && empty.Days[i-1].Date >= day.Date) {
				t.Fatal("invalid empty history", day)
			}
		}
	}
	// Verify the fixture actually exercised rows from two physical partitions.
	var partitions int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT tableoid) FROM traffic_daily WHERE user_id=$1 AND stat_date >= '2088-12-30' AND stat_date < '2089-01-02'`, a).Scan(&partitions); err != nil || partitions != 2 {
		t.Fatal("test did not traverse both partitions", partitions, err)
	}

	t.Run("large SQL aggregates and corrupt counters", func(t *testing.T) {
		for _, tc := range []struct {
			name                   string
			up1, down1, up2, down2 int64
			wantError              bool
		}{
			{"safe upper bound", maxSafeBytes, 0, 0, 0, false},
			{"numeric sum exceeds int64", math.MaxInt64, 0, math.MaxInt64, 0, true},
			{"device sum exceeds safe bound", maxSafeBytes, 1, 0, 0, true},
			{"negative row cannot cancel", -1, 0, 2, 0, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				user := newTrafficUser(t, ctx, tx)
				device := newTrafficDevice(t, ctx, tx, user, "ACTIVE")
				addTraffic(t, ctx, tx, "2089-01-01", user, device, 1, tc.up1, tc.down1)
				addTraffic(t, ctx, tx, "2089-01-01", user, device, 2, tc.up2, tc.down2)
				usage, err := svc.Daily(ctx, user, 1)
				if tc.wantError {
					if err == nil || usage.Days != nil {
						t.Fatal("unsafe aggregate returned", usage, err)
					}
				} else if err != nil || usage.Days[0].UpBytes != maxSafeBytes || usage.Days[0].ByDevice[0].Bytes != maxSafeBytes {
					t.Fatal("safe boundary was changed", usage, err)
				}
			})
		}
	})
}
