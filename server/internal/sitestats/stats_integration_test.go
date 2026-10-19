package sitestats

import (
	"context"
	"crypto/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
)

func TestPageviewAndDownloadPersistIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		t.Fatal(err)
	}
	counter := &PGCounter{Pool: pool}
	svc := &Service{Counter: counter, Redis: allowAll{}, Pepper: pepper, Clock: clock.Real{}}
	day := KLWindows(time.Now()).Today
	viewsBefore := metricCount(t, pool, day, "pageview")
	downloadsBefore := metricCount(t, pool, day, "download_android")
	const ip = "198.51.100.20"
	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	svc.NotePageview(ctx, ip, ua)
	svc.NotePageview(ctx, ip, ua)
	path, ok := svc.NoteDownload(ctx, "android", ip, ua)
	if !ok || path == "" {
		t.Fatal(path, ok)
	}
	if _, ok = svc.NoteDownload(ctx, "android", ip, ua); !ok {
		t.Fatal("second download must still redirect")
	}
	if got := metricCount(t, pool, day, "pageview"); got != viewsBefore+2 {
		t.Fatalf("pageviews %d -> %d", viewsBefore, got)
	}
	if got := metricCount(t, pool, day, "download_android"); got != downloadsBefore+1 {
		t.Fatalf("same hash must count one android download per day: %d -> %d", downloadsBefore, got)
	}
	var visitors int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM site_visitor_days WHERE day=$1`, day).Scan(&visitors); err != nil || visitors < 1 {
		t.Fatal(visitors, err)
	}
}

func metricCount(t *testing.T, pool *pgxpool.Pool, day time.Time, metric string) int {
	t.Helper()
	var raw string
	err := pool.QueryRow(context.Background(), `SELECT coalesce(sum(count),0)::text FROM site_daily_metrics WHERE day=$1 AND metric=$2`, day, metric).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
