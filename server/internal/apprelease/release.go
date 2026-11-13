package apprelease

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/admin"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

type Release struct {
	ID                int64     `json:"id"`
	Platform          string    `json:"platform"`
	Version           string    `json:"version"`
	Build             int       `json:"build"`
	URL               string    `json:"url"`
	SHA256            string    `json:"sha256"`
	Size              int64     `json:"size"`
	ReleasedAt        time.Time `json:"released_at"`
	MinSupportedBuild int       `json:"min_supported_build"`
	Notes             string    `json:"notes"`
	CertSHA256        *string   `json:"cert_sha256,omitempty"`
	Current           bool      `json:"current"`
}

type Public struct {
	Version           string    `json:"version"`
	Build             int       `json:"build"`
	URL               string    `json:"url"`
	SHA256            string    `json:"sha256"`
	Size              int64     `json:"size"`
	ReleasedAt        time.Time `json:"released_at"`
	MinSupportedBuild int       `json:"min_supported_build"`
	Notes             string    `json:"notes"`
	CertSHA256        *string   `json:"cert_sha256,omitempty"`
}

func (r Release) Public() Public {
	return Public{
		Version: r.Version, Build: r.Build, URL: r.URL, SHA256: strings.ToLower(r.SHA256),
		Size: r.Size, ReleasedAt: r.ReleasedAt.UTC(), MinSupportedBuild: r.MinSupportedBuild,
		Notes: r.Notes, CertSHA256: lowerPtr(r.CertSHA256),
	}
}

func lowerPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.ToLower(*v)
	return &s
}

func AllowedURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Host == "" {
		return false
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "yhvpn.io", "www.yhvpn.io", "api.yhvpn.io":
		return true
	default:
		return false
	}
}

type Input struct {
	Platform          string  `json:"platform"`
	Version           string  `json:"version"`
	Build             int     `json:"build"`
	URL               string  `json:"url"`
	SHA256            string  `json:"sha256"`
	Size              int64   `json:"size"`
	MinSupportedBuild int     `json:"min_supported_build"`
	Notes             string  `json:"notes"`
	CertSHA256        *string `json:"cert_sha256"`
}

func (in Input) validate() error {
	switch in.Platform {
	case "windows", "android":
	default:
		return httpx.NewError(httpx.ErrBadRequest, "平台只能是 windows 或 android")
	}
	if strings.TrimSpace(in.Version) == "" || len(in.Version) > 32 || in.Build < 1 || in.Size < 1 {
		return httpx.NewError(httpx.ErrBadRequest, "版本、构建号或大小无效")
	}
	if in.MinSupportedBuild < 1 || in.MinSupportedBuild > in.Build {
		return httpx.NewError(httpx.ErrBadRequest, "最低可用构建号无效")
	}
	if !shaPattern.MatchString(in.SHA256) || (in.CertSHA256 != nil && !shaPattern.MatchString(*in.CertSHA256)) {
		return httpx.NewError(httpx.ErrBadRequest, "校验值必须是 64 位十六进制")
	}
	if !AllowedURL(in.URL) {
		return httpx.NewError(httpx.ErrBadRequest, "下载地址必须是 yhvpn.io 或 api.yhvpn.io 的 https 链接")
	}
	if len(in.Notes) > 8000 {
		return httpx.NewError(httpx.ErrBadRequest, "更新说明太长")
	}
	return nil
}

type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Begin(context.Context) (pgx.Tx, error)
}

type Service struct {
	Pool  Querier
	Clock clock.Clock
}

func (s *Service) Current(ctx context.Context, platform string) (Release, error) {
	if platform != "windows" && platform != "android" {
		return Release{}, httpx.NewError(httpx.ErrBadRequest, "平台只能是 windows 或 android")
	}
	row := s.Pool.QueryRow(ctx, `SELECT id, platform, version, build, url, sha256, size_bytes, released_at, min_supported_build, notes, cert_sha256, is_current
FROM app_releases WHERE platform=$1 AND is_current ORDER BY build DESC LIMIT 1`, platform)
	return scanRelease(row)
}

func (s *Service) List(ctx context.Context, platform string) ([]Release, error) {
	if platform != "" && platform != "windows" && platform != "android" {
		return nil, httpx.NewError(httpx.ErrBadRequest, "平台只能是 windows 或 android")
	}
	query := `SELECT id, platform, version, build, url, sha256, size_bytes, released_at, min_supported_build, notes, cert_sha256, is_current
FROM app_releases`
	var args []any
	if platform != "" {
		query += ` WHERE platform=$1`
		args = append(args, platform)
	}
	query += ` ORDER BY platform, build DESC`
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		item, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) Publish(ctx context.Context, in Input) (Release, error) {
	if err := in.validate(); err != nil {
		return Release{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE app_releases SET is_current=false WHERE platform=$1 AND is_current`, in.Platform); err != nil {
		return Release{}, err
	}
	now := s.Clock.Now().UTC()
	row := tx.QueryRow(ctx, `INSERT INTO app_releases(platform, version, build, url, sha256, size_bytes, released_at, min_supported_build, notes, cert_sha256, is_current)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true)
ON CONFLICT (platform, build) DO UPDATE SET version=EXCLUDED.version, url=EXCLUDED.url, sha256=EXCLUDED.sha256, size_bytes=EXCLUDED.size_bytes,
 released_at=EXCLUDED.released_at, min_supported_build=EXCLUDED.min_supported_build, notes=EXCLUDED.notes, cert_sha256=EXCLUDED.cert_sha256, is_current=true
RETURNING id, platform, version, build, url, sha256, size_bytes, released_at, min_supported_build, notes, cert_sha256, is_current`,
		in.Platform, strings.TrimSpace(in.Version), in.Build, in.URL, strings.ToLower(in.SHA256), in.Size, now, in.MinSupportedBuild, in.Notes, lowerPtr(in.CertSHA256))
	item, err := scanRelease(row)
	if err != nil {
		return Release{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return item, nil
}

func (s *Service) Rollback(ctx context.Context, id int64) (Release, error) {
	if id < 1 {
		return Release{}, httpx.NewError(httpx.ErrBadRequest, "版本编号无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback(ctx)
	var platform string
	if err = tx.QueryRow(ctx, `SELECT platform FROM app_releases WHERE id=$1`, id).Scan(&platform); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Release{}, httpx.NewError(httpx.ErrNotFound, "没有这个安装包")
		}
		return Release{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE app_releases SET is_current=false WHERE platform=$1 AND is_current`, platform); err != nil {
		return Release{}, err
	}
	row := tx.QueryRow(ctx, `UPDATE app_releases SET is_current=true WHERE id=$1
RETURNING id, platform, version, build, url, sha256, size_bytes, released_at, min_supported_build, notes, cert_sha256, is_current`, id)
	item, err := scanRelease(row)
	if err != nil {
		return Release{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Release{}, err
	}
	return item, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRelease(row scanner) (Release, error) {
	var item Release
	err := row.Scan(&item.ID, &item.Platform, &item.Version, &item.Build, &item.URL, &item.SHA256, &item.Size, &item.ReleasedAt, &item.MinSupportedBuild, &item.Notes, &item.CertSHA256, &item.Current)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, httpx.NewError(httpx.ErrNotFound, "还没有发布安装包")
	}
	return item, err
}

type Handler struct {
	Svc   *Service
	Token string
}

func (h *Handler) Latest(c *gin.Context) {
	allowSite(c)
	platform := c.Query("platform")
	item, err := h.Svc.Current(c.Request.Context(), platform)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	httpx.OK(c, item.Public())
}

func (h *Handler) Redirect(c *gin.Context) {
	item, err := h.Svc.Current(c.Request.Context(), c.Param("platform"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.Redirect(http.StatusFound, item.URL)
}

func (h *Handler) Publish(c *gin.Context) {
	if !tokenOK(c.GetHeader("Authorization"), h.Token) {
		httpx.Fail(c, httpx.NewError(httpx.ErrTokenMissing, "发布令牌无效"))
		return
	}
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	item, err := h.Svc.Publish(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, item.Public())
}

func (h *Handler) List(c *gin.Context) {
	items, err := h.Svc.List(c.Request.Context(), c.Query("platform"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if items == nil {
		items = []Release{}
	}
	httpx.OK(c, items)
}

func (h *Handler) Rollback(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "版本编号无效"))
		return
	}
	item, err := h.Svc.Rollback(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, item.Public())
}

func tokenOK(header, want string) bool {
	const prefix = "Bearer "
	if want == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func allowSite(c *gin.Context) {
	switch c.GetHeader("Origin") {
	case "https://yhvpn.io", "https://www.yhvpn.io":
		c.Header("Access-Control-Allow-Origin", c.GetHeader("Origin"))
		c.Header("Vary", "Origin")
	}
}

// RegisterAdmin mounts the session-backed list and rollback routes.
func (h *Handler) RegisterAdmin(group *gin.RouterGroup, authn *admin.Handler) {
	group.GET("/app/releases", authn.Middleware(), admin.RequirePermission("app_versions.read"), h.List)
	group.POST("/app/releases/:id/current", authn.Middleware(), admin.RequirePermission("app_versions.publish"), h.Rollback)
}
