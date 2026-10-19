package sitestats

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/clock"
)

type fixedClock time.Time

func (f fixedClock) Now() time.Time { return time.Time(f) }

type allowAll struct{}

func (allowAll) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return true, 0, nil
}

type denyGate struct{}

func (denyGate) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, time.Second, nil
}

type downGate struct{}

func (downGate) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, 0, context.DeadlineExceeded
}

type memoryCounter struct {
	views     int
	downloads int
}

func (m *memoryCounter) Pageview(context.Context, time.Time, []byte) error {
	m.views++
	return nil
}

func (m *memoryCounter) Download(context.Context, time.Time, string, string, []byte) (bool, error) {
	m.downloads++
	return true, nil
}

func TestKLWindowsMondayAndPreviousMonth(t *testing.T) {
	// 2026-10-19 是星期一，吉隆坡 03:00 仍是 18 日 UTC。
	now := time.Date(2026, 10, 18, 19, 30, 0, 0, time.UTC)
	w := KLWindows(now)
	if w.LocationName != "Asia/Kuala_Lumpur" || w.Today.Format(time.DateOnly) != "2026-10-19" {
		t.Fatalf("today %+v", w)
	}
	if w.WeekStart.Format(time.DateOnly) != "2026-10-19" || w.MonthStart.Day() != 1 {
		t.Fatalf("week/month %+v", w)
	}
	if w.PreviousStart.Format(time.DateOnly) != "2026-09-01" || w.PreviousEnd.Format(time.DateOnly) != "2026-09-30" {
		t.Fatalf("previous month %+v", w)
	}
	sunday := time.Date(2026, 10, 18, 10, 0, 0, 0, kualaLumpur)
	week := KLWindows(sunday)
	if week.Today.Format(time.DateOnly) != "2026-10-18" || week.WeekStart.Format(time.DateOnly) != "2026-10-12" {
		t.Fatalf("sunday belongs to the previous Monday: %+v", week)
	}
}

func TestHashIPDoesNotContainAddress(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	ip := "203.0.113.10"
	sum := HashIP(pepper, ip)
	if len(sum) != 32 || bytes.Contains(sum, []byte(ip)) || bytes.Equal(sum, HashIP(pepper, "203.0.113.11")) {
		t.Fatalf("hash %x", sum)
	}
	if HashIP(nil, ip) != nil || HashIP(pepper, "") != nil {
		t.Fatal("empty pepper or address must not be hashed")
	}
}

func TestBotsAndChannels(t *testing.T) {
	if !IsBot("") || !IsBot("curl/8.0") || !IsBot("Mozilla/5.0 (compatible; Googlebot/2.1)") {
		t.Fatal("obvious bots should be ignored")
	}
	if IsBot("Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/120.0.0.0 Mobile") {
		t.Fatal("a normal phone browser is not a bot")
	}
	android, ok := Target("android")
	windows, okWin := Target("windows")
	legacy, okOld := Target("android-legacy")
	if !ok || !okWin || !okOld || android.Path == windows.Path || android.Metric == legacy.Metric {
		t.Fatalf("channels %#v %#v %#v", android, windows, legacy)
	}
	if _, ok := Target("../etc"); ok {
		t.Fatal("unknown channel")
	}
}

func TestPageviewCountsHumansAndSkipsBots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	counter := &memoryCounter{}
	pepper := bytes.Repeat([]byte{9}, 32)
	svc := &Service{Counter: counter, Redis: allowAll{}, Pepper: pepper, Clock: fixedClock(time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC))}
	h := &Handler{Svc: svc}
	r := gin.New()
	h.Mount(r.Group("/v1"))

	human := httptest.NewRequest(http.MethodPost, "/v1/site/pageview", strings.NewReader(`{"path":"/download/"}`))
	human.Header.Set("Origin", "https://yhvpn.io")
	human.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	human.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, human)
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://yhvpn.io" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("human %d %v", w.Code, w.Header())
	}
	if counter.views != 1 {
		t.Fatalf("views %d", counter.views)
	}

	bot := httptest.NewRequest(http.MethodPost, "/v1/site/pageview", nil)
	bot.Header.Set("User-Agent", "curl/8.4.0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, bot)
	if w.Code != http.StatusNoContent || counter.views != 1 {
		t.Fatalf("bot was counted: %d views %d", w.Code, counter.views)
	}

	foreign := httptest.NewRequest(http.MethodPost, "/v1/site/pageview", nil)
	foreign.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, foreign)
	if w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign origin leaked CORS: %d %q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}

	svc.Redis = downGate{}
	again := httptest.NewRequest(http.MethodPost, "/v1/site/pageview", nil)
	again.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, again)
	if w.Code != http.StatusNoContent || counter.views != 1 {
		t.Fatalf("redis down must not count: %d %d", w.Code, counter.views)
	}
	svc.Redis = denyGate{}
	limited := httptest.NewRequest(http.MethodPost, "/v1/site/pageview", nil)
	limited.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, limited)
	if w.Code != http.StatusNoContent || counter.views != 1 {
		t.Fatalf("rate limit must not count: %d %d", w.Code, counter.views)
	}
}

func TestDownloadRedirectsAndSeparatesLegacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	counter := &memoryCounter{}
	svc := &Service{Counter: counter, Redis: allowAll{}, Pepper: bytes.Repeat([]byte{3}, 32), Clock: clock.Real{}}
	r := gin.New()
	(&Handler{Svc: svc}).Mount(r.Group("/v1"))
	req := httptest.NewRequest(http.MethodGet, "/v1/site/downloads/android-legacy", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/120.0.0.0")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound || !strings.HasSuffix(w.Header().Get("Location"), "/download/yuhai-vpn-1.1.0.apk") || counter.downloads != 1 {
		t.Fatalf("legacy %d %q downloads %d", w.Code, w.Header().Get("Location"), counter.downloads)
	}
	missing := httptest.NewRequest(http.MethodGet, "/v1/site/downloads/macos", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, missing)
	if w.Code == http.StatusFound {
		t.Fatal("unknown channel redirected")
	}
}

func TestPreflightAllowsBothSiteOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	(&Handler{}).Mount(r.Group("/v1"))
	for _, origin := range []string{"https://yhvpn.io", "https://www.yhvpn.io"} {
		req := httptest.NewRequest(http.MethodOptions, "/v1/site/pageview", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "Content-Type")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%s %d %v", origin, w.Code, w.Header())
		}
	}
}
