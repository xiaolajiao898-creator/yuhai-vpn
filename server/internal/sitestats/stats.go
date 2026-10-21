// Package sitestats 记录官网浏览和安装包下载，并按吉隆坡自然日汇总。
//
// 只保存 IP 的 HMAC，不写明文地址，也不把 IP 放进日志。Redis 不可用时不计数
// （统计失败关闭），但浏览仍返回 204、下载仍跳转到静态文件。
package sitestats

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redisx"
)

const (
	metricPageview       = "pageview"
	metricAndroid        = "download_android"
	metricWindows        = "download_windows"
	metricAndroidLegacy  = "download_android_legacy"
	channelAndroid       = "android"
	channelWindows       = "windows"
	channelAndroidLegacy = "android-legacy"
	siteOrigin           = "https://yhvpn.io"
	downloadHost         = "https://yhvpn.io"
	hashDomain           = "site-stats-v1\n"
	pageviewGap          = 30 * time.Second
	pageviewDailyCap     = 30
)

// Windows 是吉隆坡时区的统计窗口。历史访客不是每日访客之和。
type Windows struct {
	Today         time.Time
	WeekStart     time.Time
	MonthStart    time.Time
	PreviousStart time.Time
	PreviousEnd   time.Time
	LocationName  string
}

type DownloadTarget struct {
	Channel string
	Metric  string
	Path    string
}

var downloadTargets = map[string]DownloadTarget{
	channelAndroid:       {Channel: channelAndroid, Metric: metricAndroid, Path: "/download/yuhai-vpn-candidate-0928.apk"},
	channelWindows:       {Channel: channelWindows, Metric: metricWindows, Path: "/download/YuhaiVPN-Windows-x64-candidate.exe"},
	channelAndroidLegacy: {Channel: channelAndroidLegacy, Metric: metricAndroidLegacy, Path: "/download/yuhai-vpn-1.1.0.apk"},
}

var kualaLumpur = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kuala_Lumpur")
	if err != nil {
		panic(err)
	}
	return loc
}()

var botMarkers = []string{
	"bot", "spider", "crawl", "slurp", "wget", "curl", "python-requests",
	"go-http-client", "headless", "scrapy", "semrush", "ahrefs", "petalbot",
	"bytespider", "gptbot", "claudebot", "bingpreview", "facebookexternalhit",
}

var allowedOrigins = map[string]bool{
	"https://yhvpn.io":     true,
	"https://www.yhvpn.io": true,
}

// KLWindows 把 now 折到吉隆坡自然日。一周从星期一开始，上月是完整的上一个日历月。
func KLWindows(now time.Time) Windows {
	local := now.In(kualaLumpur)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, kualaLumpur)
	weekday := int(today.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekStart := today.AddDate(0, 0, -(weekday - 1))
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, kualaLumpur)
	previousEnd := monthStart.AddDate(0, 0, -1)
	previousStart := time.Date(previousEnd.Year(), previousEnd.Month(), 1, 0, 0, 0, 0, kualaLumpur)
	return Windows{
		Today: today, WeekStart: weekStart, MonthStart: monthStart,
		PreviousStart: previousStart, PreviousEnd: previousEnd, LocationName: kualaLumpur.String(),
	}
}

// HashIP 用密钥胡椒做 HMAC。空胡椒或空地址返回 nil，调用方不得计数。
func HashIP(pepper []byte, ip string) []byte {
	if len(pepper) == 0 || ip == "" {
		return nil
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(hashDomain))
	_, _ = mac.Write([]byte(ip))
	return mac.Sum(nil)
}

func IsBot(userAgent string) bool {
	ua := strings.TrimSpace(userAgent)
	if len(ua) < 16 {
		return true
	}
	lower := strings.ToLower(ua)
	for _, marker := range botMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func Target(channel string) (DownloadTarget, bool) {
	t, ok := downloadTargets[channel]
	return t, ok
}

// gate 是滑动窗口。Redis 故障由调用方决定不计数。
type gate interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error)
}

// Counter 落库。Download 返回这次是否新计入（同一哈希、同一通道、同一吉隆坡日只计一次）。
type Counter interface {
	Pageview(ctx context.Context, day time.Time, hash []byte) error
	Download(ctx context.Context, day time.Time, channel, metric string, hash []byte) (bool, error)
}

type Service struct {
	Counter Counter
	Redis   gate
	Pepper  []byte
	Clock   clock.Clock
}

func (s *Service) now() time.Time {
	if s == nil || s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

func (s *Service) allowPageview(ctx context.Context, day time.Time, hash []byte) bool {
	if s == nil || s.Redis == nil || len(hash) != sha256.Size {
		return false
	}
	hexHash := hex.EncodeToString(hash)
	ok, _, err := s.Redis.Allow(ctx, "site:pv:30s:"+hexHash, 1, pageviewGap)
	if err != nil || !ok {
		return false
	}
	ok, _, err = s.Redis.Allow(ctx, "site:pv:day:"+day.Format(time.DateOnly)+":"+hexHash, pageviewDailyCap, 24*time.Hour)
	return err == nil && ok
}

// NotePageview 在允许时记一次浏览。机器人和限流都不记，调用方始终返回 204。
func (s *Service) NotePageview(ctx context.Context, ip, userAgent string) {
	if s == nil || s.Counter == nil || IsBot(userAgent) {
		return
	}
	hash := HashIP(s.Pepper, ip)
	if hash == nil {
		return
	}
	day := KLWindows(s.now()).Today
	if !s.allowPageview(ctx, day, hash) {
		return
	}
	_ = s.Counter.Pageview(ctx, day, hash)
}

// NoteDownload 返回跳转路径。不计次时仍然返回路径，让直接下载继续可用。
func (s *Service) NoteDownload(ctx context.Context, channel, ip, userAgent string) (string, bool) {
	target, ok := Target(channel)
	if !ok {
		return "", false
	}
	if s != nil && s.Counter != nil && !IsBot(userAgent) {
		if hash := HashIP(s.Pepper, ip); hash != nil && s.Redis != nil {
			day := KLWindows(s.now()).Today
			hexHash := hex.EncodeToString(hash)
			allowed, _, err := s.Redis.Allow(ctx, "site:dl:"+channel+":"+day.Format(time.DateOnly)+":"+hexHash, 1, 24*time.Hour)
			if err == nil && allowed {
				_, _ = s.Counter.Download(ctx, day, channel, target.Metric, hash)
			}
		}
	}
	return downloadHost + target.Path, true
}

type PGCounter struct{ Pool *pgxpool.Pool }

func (c *PGCounter) Pageview(ctx context.Context, day time.Time, hash []byte) error {
	if c == nil || c.Pool == nil || len(hash) != sha256.Size {
		return errors.New("site stats unavailable")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO site_visitor_days(day, ip_hash) VALUES ($1,$2) ON CONFLICT DO NOTHING`, day, hash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO site_visitors(ip_hash, first_day) VALUES ($1,$2) ON CONFLICT DO NOTHING`, hash, day); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO site_daily_metrics(day, metric, count) VALUES ($1,$2,1)
 ON CONFLICT (day, metric) DO UPDATE SET count = site_daily_metrics.count + 1`, day, metricPageview); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (c *PGCounter) Download(ctx context.Context, day time.Time, channel, metric string, hash []byte) (bool, error) {
	if c == nil || c.Pool == nil || len(hash) != sha256.Size {
		return false, errors.New("site stats unavailable")
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `INSERT INTO site_download_dedupe(day, channel, ip_hash) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, day, channel, hash)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, `INSERT INTO site_daily_metrics(day, metric, count) VALUES ($1,$2,1)
 ON CONFLICT (day, metric) DO UPDATE SET count = site_daily_metrics.count + 1`, day, metric); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MetricSum 汇总闭区间 [from, to]。from 为零值表示不设下界，to 为零值表示不设上界。
func MetricSum(ctx context.Context, tx pgx.Tx, metric string, from, to time.Time) (string, error) {
	var count string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(count),0)::text FROM site_daily_metrics
 WHERE metric=$1 AND ($2::date IS NULL OR day>=$2::date) AND ($3::date IS NULL OR day<=$3::date)`,
		metric, nullableDate(from), nullableDate(to)).Scan(&count)
	return count, err
}

func nullableDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

type Handler struct {
	Svc *Service
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	v1.POST("/site/pageview", h.pageview)
	v1.OPTIONS("/site/pageview", h.preflight)
	v1.GET("/site/downloads/:channel", h.download)
}

func (h *Handler) pageview(c *gin.Context) {
	if !originAllowed(c) {
		httpx.Fail(c, httpx.NewError(httpx.ErrForbidden, "来源不在允许名单"))
		return
	}
	writeCORS(c)
	if !acceptPageviewBody(c) {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "浏览上报格式无效"))
		return
	}
	if h != nil && h.Svc != nil {
		h.Svc.NotePageview(c.Request.Context(), auth.ClientIP(c), c.Request.UserAgent())
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) preflight(c *gin.Context) {
	if !preflightAllowed(c) {
		httpx.Fail(c, httpx.NewError(httpx.ErrForbidden, "来源不在允许名单"))
		return
	}
	writeCORS(c)
	c.Status(http.StatusNoContent)
}

func (h *Handler) download(c *gin.Context) {
	path, ok := "", false
	if h != nil && h.Svc != nil {
		path, ok = h.Svc.NoteDownload(c.Request.Context(), c.Param("channel"), auth.ClientIP(c), c.Request.UserAgent())
	} else if target, known := Target(c.Param("channel")); known {
		path, ok = downloadHost+target.Path, true
	}
	if !ok {
		httpx.Fail(c, httpx.NewError(httpx.ErrNotFound, "没有这个下载通道"))
		return
	}
	c.Redirect(http.StatusFound, path)
}

func originAllowed(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if origin == "" {
		return true
	}
	return allowedOrigins[origin]
}

func writeCORS(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if !allowedOrigins[origin] {
		return
	}
	c.Header("Access-Control-Allow-Origin", origin)
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	c.Header("Access-Control-Max-Age", "600")
	c.Header("Vary", "Origin")
}

func preflightAllowed(c *gin.Context) bool {
	if !allowedOrigins[c.GetHeader("Origin")] {
		return false
	}
	if strings.ToUpper(strings.TrimSpace(c.GetHeader("Access-Control-Request-Method"))) != http.MethodPost {
		return false
	}
	raw := c.GetHeader("Access-Control-Request-Headers")
	if strings.TrimSpace(raw) == "" {
		return true
	}
	for _, header := range strings.Split(raw, ",") {
		header = strings.TrimSpace(strings.ToLower(header))
		if header == "" {
			continue
		}
		if header != "content-type" {
			return false
		}
	}
	return true
}

func acceptPageviewBody(c *gin.Context) bool {
	if c.Request.Body == nil || c.Request.ContentLength == 0 {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
	if err != nil || len(raw) > 1024 {
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body struct {
		Path string `json:"path"`
	}
	if dec.Decode(&body) != nil || dec.More() {
		return false
	}
	return validPath(body.Path)
}

func validPath(path string) bool {
	if path == "" {
		return true
	}
	if len(path) > 128 || !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "://") {
		return false
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	return true
}

// Redis 适配：生产用 redisx.Client，它已经实现 gate。
var _ gate = (*redisx.Client)(nil)
