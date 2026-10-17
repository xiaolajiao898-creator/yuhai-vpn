package appinfo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type queryFunc func(context.Context, string, ...any) pgx.Row

func (q queryFunc) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return q(ctx, sql, args...)
}

type versionRow struct {
	version Version
	err     error
}

func (r versionRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	v := r.version
	values := []any{v.Latest.VersionName, v.Latest.VersionCode, v.Minimum, v.Latest.PublishedAt, v.Latest.Notes, v.Latest.DownloadURL, v.Latest.SHA256, v.Latest.CertSHA256}
	for i, value := range values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func releasedVersion(now time.Time) Version {
	return Version{Platform: "ANDROID", Minimum: 50, Latest: Release{VersionName: "1.0.0", VersionCode: 100, PublishedAt: now, Notes: "正式更新说明"}}
}

type testReader struct {
	calls    int
	platform string
	current  int
	err      error
}

func (r *testReader) Latest(_ context.Context, platform string, current int) (Version, error) {
	r.calls++
	r.platform, r.current = platform, current
	return releasedVersion(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)), r.err
}

func versionRequest(r *gin.Engine, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/app/version?"+query, nil))
	return w
}

func TestVersionParametersAndPublicCache(t *testing.T) {
	reader := &testReader{}
	r := gin.New()
	r.GET("/app/version", (&Handler{Svc: reader}).GetVersion)
	for _, tc := range []struct {
		query    string
		platform string
		current  int
	}{
		{"platform=ANDROID", "ANDROID", 0},
		{"platform=ANDROID&current_code=0", "ANDROID", 0},
		{"platform=IOS&current_code=1", "IOS", 1},
		{"platform=IOS&current_code=2147483647", "IOS", 2147483647},
	} {
		w := versionRequest(r, tc.query)
		if w.Code != http.StatusOK || reader.platform != tc.platform || reader.current != tc.current || w.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Fatal("incorrect public version parameters", tc, w.Code, reader.platform, reader.current, w.Body.String())
		}
	}
	before := reader.calls
	for _, query := range []string{"", "platform=", "platform=android", "platform=WINDOWS", "platform=ANDROID&platform=IOS", "platform=ANDROID&platform=ANDROID", "platform=ANDROID&current_code=", "platform=ANDROID&current_code=-1", "platform=ANDROID&current_code=%2B1", "platform=ANDROID&current_code=01", "platform=ANDROID&current_code=1.0", "platform=ANDROID&current_code=1%20", "platform=ANDROID&current_code=2147483648", "platform=ANDROID&current_code=0&current_code=1", "platform=ANDROID&current_code=%ZZ", "platform=ANDROID&current_code=1;current_code=2"} {
		w := versionRequest(r, query)
		if w.Code != http.StatusBadRequest || reader.calls != before || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("invalid version query reached service: %q status=%d calls=%d", query, w.Code, reader.calls)
		}
	}
}

func TestVersionErrorsDoNotFabricateReleaseOrExposeDatabase(t *testing.T) {
	reader := &testReader{}
	r := gin.New()
	r.GET("/app/version", (&Handler{Svc: reader}).GetVersion)
	for _, tc := range []struct {
		err  error
		code int
	}{
		{httpx.NewError(httpx.ErrNotFound, "尚未发布正式版本"), 404},
		{errors.New("private database connection details"), 500},
	} {
		reader.err = tc.err
		w := versionRequest(r, "platform=ANDROID")
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), `"latest"`) || strings.Contains(w.Body.String(), "private database") {
			t.Fatal("error fabricated a release or exposed database", w.Code, w.Body.String())
		}
	}
}

func TestVersionForceUpdateBoundaryAndQueryDeadline(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	svc := &Service{Clock: fixedClock{now}, Pool: queryFunc(func(ctx context.Context, _ string, args ...any) pgx.Row {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second || !reflect.DeepEqual(args, []any{"ANDROID", now}) {
			t.Fatal("query lost deadline or platform/time parameters", args, deadline)
		}
		return versionRow{version: releasedVersion(now)}
	})}
	for _, tc := range []struct {
		current int
		force   bool
	}{{0, false}, {1, true}, {49, true}, {50, false}, {100, false}, {101, false}, {2147483647, false}} {
		result, err := svc.Latest(context.Background(), "ANDROID", tc.current)
		if err != nil || result.ForceUpdate != tc.force || result.Minimum != 50 || result.Latest.VersionCode != 100 {
			t.Fatal("wrong forced-upgrade boundary", tc, result, err)
		}
	}
	invalid := &Service{}
	for _, tc := range []struct {
		platform string
		current  int
	}{{"", 0}, {"LINUX", 1}, {"ANDROID", -1}, {"IOS", 2147483648}} {
		if _, err := invalid.Latest(context.Background(), tc.platform, tc.current); err == nil {
			t.Fatal("invalid service arguments reached query", tc)
		}
	}
}

func TestVersionRejectsInvalidReleaseMetadata(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct{ latest, minimum int }{{0, 0}, {-1, 0}, {100, -1}, {100, 101}} {
		v := releasedVersion(now)
		v.Latest.VersionCode, v.Minimum = tc.latest, tc.minimum
		svc := &Service{Clock: fixedClock{now}, Pool: queryFunc(func(context.Context, string, ...any) pgx.Row { return versionRow{version: v} })}
		result, err := svc.Latest(context.Background(), "ANDROID", 1)
		var apiErr *httpx.Error
		if !errors.As(err, &apiErr) || apiErr.Code != httpx.ErrMaintenance || result.Latest.VersionCode != 0 {
			t.Fatal("invalid release escaped validation", tc, result, err)
		}
	}
	svc := &Service{Clock: fixedClock{now}, Pool: queryFunc(func(context.Context, string, ...any) pgx.Row { return versionRow{err: pgx.ErrNoRows} })}
	result, err := svc.Latest(context.Background(), "IOS", 0)
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != httpx.ErrNotFound || result.Latest.VersionName != "" || result.Latest.VersionCode != 0 || result.Latest.DownloadURL != nil {
		t.Fatal("missing release fabricated a fallback", result, err)
	}
}

func TestVersionDoesNotReturnUnsafeDownloadURLs(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		url  string
		keep bool
	}{
		{"https://download.example.com/yuhai.apk", true},
		{"https://testflight.apple.com/j/example", true},
		{"HTTPS://download.example.com/yuhai.apk", true},
		{"https://download.example.com:8443/yuhai.apk", true},
		{"", false}, {"http://download.example.com/yuhai.apk", false},
		{"javascript:alert(1)", false}, {"//download.example.com/yuhai.apk", false},
		{"/yuhai.apk", false}, {"https:///yuhai.apk", false}, {"https://", false},
		{"https://user:password@download.example.com/yuhai.apk", false},
		{"https://download.example.com:0/yuhai.apk", false},
		{"https://download.example.com:65536/yuhai.apk", false},
		{"https://download.example.com:invalid/yuhai.apk", false},
		{"https://download.example.com/\nunsafe.apk", false},
		{"https://download.example.com/%ZZ", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			v := releasedVersion(now)
			v.Latest.DownloadURL = &tc.url
			svc := &Service{Clock: fixedClock{now}, Pool: queryFunc(func(context.Context, string, ...any) pgx.Row { return versionRow{version: v} })}
			result, err := svc.Latest(context.Background(), "ANDROID", 100)
			if err != nil || (result.Latest.DownloadURL != nil) != tc.keep {
				t.Fatal("wrong download URL filtering", tc.url, result.Latest.DownloadURL, err)
			}
		})
	}
}
