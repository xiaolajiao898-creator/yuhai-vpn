package appinfo

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func TestVersionPublicationAndPlatformFiltersIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Session-local clone retains the real schema and constraints. No fixture
	// release is ever written to public.app_versions or visible to the API.
	if _, err := tx.Exec(ctx, `CREATE TEMPORARY TABLE app_versions (LIKE public.app_versions INCLUDING ALL) ON COMMIT DROP`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	svc := &Service{Pool: tx, Clock: fixedClock{now}}
	add := func(platform string, code, minimum int, status string, published *time.Time, link *string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `INSERT INTO app_versions(platform,version_name,version_code,min_supported_code,status,published_at,download_url,release_notes) VALUES($1,'test-release',$2,$3,$4,$5,$6,'test notes')`, platform, code, minimum, status, published, link); err != nil {
			t.Fatal(err)
		}
	}
	missing := func() {
		t.Helper()
		result, err := svc.Latest(ctx, "ANDROID", 0)
		var apiErr *httpx.Error
		if !errors.As(err, &apiErr) || apiErr.Code != httpx.ErrNotFound || result.Latest.VersionCode != 0 || result.Latest.DownloadURL != nil {
			t.Fatal("unreleased Android version became available", result, err)
		}
	}
	missing()
	past, future := now.Add(-time.Hour), now.Add(time.Second)
	link := "https://download.example.com/yuhai.apk"
	add("ANDROID", 101, 50, "DRAFT", &past, &link)
	add("ANDROID", 102, 50, "WITHDRAWN", &past, &link)
	add("ANDROID", 103, 50, "PUBLISHED", &future, &link)
	add("ANDROID", 104, 50, "PUBLISHED", nil, &link)
	add("IOS", 1000, 900, "PUBLISHED", &past, &link)
	missing()
	add("ANDROID", 99, 50, "PUBLISHED", &now, &link)
	add("ANDROID", 100, 50, "PUBLISHED", &past, &link)
	for _, tc := range []struct {
		current int
		force   bool
	}{{0, false}, {49, true}, {50, false}, {100, false}, {101, false}} {
		result, err := svc.Latest(ctx, "ANDROID", tc.current)
		if err != nil || result.Latest.VersionCode != 100 || result.ForceUpdate != tc.force || !result.Latest.PublishedAt.Equal(past) || result.Latest.DownloadURL == nil {
			t.Fatal("incorrect latest release or force boundary", tc, result, err)
		}
	}
	ios, err := svc.Latest(ctx, "IOS", 899)
	if err != nil || ios.Platform != "IOS" || ios.Latest.VersionCode != 1000 || !ios.ForceUpdate {
		t.Fatal("platform releases mixed", ios, err)
	}
	// The exact published_at instant is inclusive; before it, code 103 was hidden.
	svc.Clock = fixedClock{future}
	result, err := svc.Latest(ctx, "ANDROID", 50)
	if err != nil || result.Latest.VersionCode != 103 || !result.Latest.PublishedAt.Equal(future) {
		t.Fatal("publication boundary was not inclusive", result, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app_versions SET status='WITHDRAWN' WHERE platform='ANDROID' AND version_code=103`); err != nil {
		t.Fatal(err)
	}
	result, err = svc.Latest(ctx, "ANDROID", 50)
	if err != nil || result.Latest.VersionCode != 100 {
		t.Fatal("withdrawn release still returned", result, err)
	}
	unsafeLink := "http://download.example.com/yuhai.apk"
	add("ANDROID", 105, 50, "PUBLISHED", &now, &unsafeLink)
	result, err = svc.Latest(ctx, "ANDROID", 50)
	if err != nil || result.Latest.VersionCode != 105 || result.Latest.DownloadURL != nil {
		t.Fatal("unsafe database download URL escaped filtering", result, err)
	}
}
