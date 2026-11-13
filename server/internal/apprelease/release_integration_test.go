package apprelease

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
)

func TestPublishAndRollbackIntegration(t *testing.T) {
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
	// yuhai_app 没有 DELETE。测完只取消“当前”标记，避免这次测试变成官网正在分发的包。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE app_releases SET is_current=false WHERE version LIKE 'test-release-%'`)
	})
	svc := &Service{Pool: pool, Clock: clock.Real{}}
	first, err := svc.Publish(ctx, Input{
		Platform: "windows", Version: "test-release-a", Build: 900001, URL: "https://yhvpn.io/download/builds/a.exe",
		SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 10,
		MinSupportedBuild: 1, Notes: "first",
	})
	if err != nil || !first.Current || first.Build != 900001 {
		t.Fatal(first, err)
	}
	second, err := svc.Publish(ctx, Input{
		Platform: "windows", Version: "test-release-b", Build: 900002, URL: "https://yhvpn.io/download/builds/b.exe",
		SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Size: 20,
		MinSupportedBuild: 900001, Notes: "second",
	})
	if err != nil || !second.Current {
		t.Fatal(second, err)
	}
	current, err := svc.Current(ctx, "windows")
	if err != nil || current.Build != 900002 {
		t.Fatal(current, err)
	}
	back, err := svc.Rollback(ctx, first.ID)
	if err != nil || !back.Current || back.Build != 900001 {
		t.Fatal(back, err)
	}
	current, err = svc.Current(ctx, "windows")
	if err != nil || current.ID != first.ID {
		t.Fatal(current, err)
	}
}
