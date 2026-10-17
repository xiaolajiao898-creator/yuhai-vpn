package appinfo

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Service struct {
	Pool  Querier
	Clock clock.Clock
}
type Release struct {
	VersionName string    `json:"version_name"`
	VersionCode int       `json:"version_code"`
	PublishedAt time.Time `json:"published_at"`
	Notes       string    `json:"notes"`
	DownloadURL *string   `json:"download_url,omitempty"`
	SHA256      *string   `json:"sha256,omitempty"`
	CertSHA256  *string   `json:"cert_fingerprint_sha256,omitempty"`
}
type Version struct {
	Platform    string  `json:"platform"`
	Latest      Release `json:"latest"`
	Minimum     int     `json:"min_supported_code"`
	ForceUpdate bool    `json:"force_update"`
}

func (s *Service) Latest(ctx context.Context, platform string, current int) (Version, error) {
	if platform != "ANDROID" && platform != "IOS" || current < 0 || int64(current) > 2147483647 {
		return Version{}, httpx.NewError(httpx.ErrBadRequest, "版本查询参数无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := Version{Platform: platform}
	err := s.Pool.QueryRow(ctx, `SELECT version_name,version_code,min_supported_code,published_at,COALESCE(release_notes,''),download_url,file_sha256,cert_sha256
 FROM app_versions WHERE platform=$1 AND status='PUBLISHED' AND published_at <= $2
 ORDER BY version_code DESC LIMIT 1`, platform, s.Clock.Now()).Scan(&result.Latest.VersionName, &result.Latest.VersionCode, &result.Minimum, &result.Latest.PublishedAt, &result.Latest.Notes, &result.Latest.DownloadURL, &result.Latest.SHA256, &result.Latest.CertSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, httpx.NewError(httpx.ErrNotFound, "尚未发布正式版本")
	}
	if err != nil {
		return result, err
	}
	if result.Latest.VersionCode <= 0 || result.Minimum < 0 || result.Minimum > result.Latest.VersionCode {
		return Version{}, httpx.NewError(httpx.ErrMaintenance, "版本信息暂不可用")
	}
	// Invalid or credential-bearing links must never become an update destination.
	if link := result.Latest.DownloadURL; link != nil {
		parsed, e := url.Parse(*link)
		if e != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
			result.Latest.DownloadURL = nil
		} else if port := parsed.Port(); port != "" {
			n, portErr := strconv.Atoi(port)
			if portErr != nil || n < 1 || n > 65535 {
				result.Latest.DownloadURL = nil
			}
		}
	}
	result.ForceUpdate = current > 0 && current < result.Minimum
	return result, nil
}

type Reader interface {
	Latest(context.Context, string, int) (Version, error)
}
type Handler struct{ Svc Reader }

func (h *Handler) GetVersion(c *gin.Context) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "版本查询参数无效"))
		return
	}
	platform, current, err := versionParams(query)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	result, err := h.Svc.Latest(c.Request.Context(), platform, current)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	httpx.OK(c, result)
}
func versionParams(q url.Values) (string, int, error) {
	bad := func() (string, int, error) {
		return "", 0, httpx.NewError(httpx.ErrBadRequest, "版本查询参数无效")
	}
	values := q["platform"]
	if len(values) != 1 || (values[0] != "ANDROID" && values[0] != "IOS") {
		return bad()
	}
	current := 0
	if codes, ok := q["current_code"]; ok {
		if len(codes) != 1 {
			return bad()
		}
		parsed, err := strconv.ParseInt(codes[0], 10, 32)
		if err != nil || parsed < 0 || strconv.FormatInt(parsed, 10) != codes[0] {
			return bad()
		}
		current = int(parsed)
	}
	return values[0], current, nil
}
