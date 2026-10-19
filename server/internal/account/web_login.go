package account

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redisx"
)

const (
	webSessionCookieName = "yh_session"
	webSessionTTL        = 12 * time.Hour

	// 只计失败。地址和 IP 各自一条，任何一条先满都拒绝，两条的提示相同。
	webLoginFailPerAddress = 10
	webLoginFailPerIP      = 30
	webLoginFailWindow     = time.Hour

	msgWebLoginInvalid  = "地址或密码错误"
	msgWebLoginLimited  = "尝试次数过多，请稍后再试"
	msgWebLoginRequired = "请先登录"

	ctxWebUser = "yh.web_user"
)

type webLoginBody struct {
	Address     string `json:"address"`
	Password    string `json:"password"`
	ChallengeID string `json:"challenge_id"`
	Counter     int64  `json:"counter"`
}

type webSessionRecord struct {
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// WebAccount 是网页用户中心需要的账户摘要。不返回令牌、余额或价格。
type WebAccount struct {
	Account       string     `json:"account"`
	PlanName      string     `json:"plan_name"`
	ExpireAt      *time.Time `json:"expire_at"`
	Active        bool       `json:"active"`
	DeviceCount   int        `json:"device_count"`
	DeviceLimit   int        `json:"device_limit"`
	TrialEligible bool       `json:"trial_eligible"`
	ServerTime    time.Time  `json:"server_time"`
}

func webSessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "websess:v1:" + hex.EncodeToString(sum[:])
}

func webLoginFailAddrKey(lookup string) string { return "weblogin_fail_addr:" + lookup }
func webLoginFailIPKey(ipHash string) string   { return "weblogin_fail_ip:" + ipHash }

func webLoginLimited(addrCount, ipCount int64) bool {
	return addrCount >= webLoginFailPerAddress || ipCount >= webLoginFailPerIP
}

func (s *Service) webLoginBlocked(ctx context.Context, lookup, ipHash string) (bool, error) {
	if s == nil || s.Redis == nil {
		return false, errors.New("redis unavailable")
	}
	addrN, err := s.Redis.WindowCount(ctx, webLoginFailAddrKey(lookup), webLoginFailWindow)
	if err != nil {
		return false, err
	}
	ipN, err := s.Redis.WindowCount(ctx, webLoginFailIPKey(ipHash), webLoginFailWindow)
	if err != nil {
		return false, err
	}
	return webLoginLimited(addrN, ipN), nil
}

func (s *Service) noteWebLoginFailure(ctx context.Context, lookup, ipHash string) error {
	if s == nil || s.Redis == nil {
		return errors.New("redis unavailable")
	}
	if _, err := s.Redis.AddWindow(ctx, webLoginFailAddrKey(lookup), webLoginFailWindow); err != nil {
		return err
	}
	_, err := s.Redis.AddWindow(ctx, webLoginFailIPKey(ipHash), webLoginFailWindow)
	return err
}

// LoginFromWeb 校验地址和密码。账户不存在、密码错误、账户停用都返回同一句提示。
// 调用方必须先通过工作量证明。这里不绑定设备，也不签发 App 令牌。
func (s *Service) LoginFromWeb(ctx context.Context, address, password, ip string) (int64, error) {
	invalid := httpx.NewError(httpx.ErrLoginInvalid, msgWebLoginInvalid)
	normalized, err := normalizeLoginAddress(address)
	if err != nil {
		return 0, err
	}
	if !utf8.ValidString(password) || len(password) > maxPasswordBytes {
		return 0, httpx.NewError(httpx.ErrBadRequest, "密码不能超过 128 个字符")
	}
	if s == nil || s.Redis == nil || s.Repo == nil || s.Repo.Pool == nil {
		return 0, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试")
	}
	lookup := loginLookup(s.Pepper, normalized)
	ipHash, _ := hashIP(ip)
	blocked, err := s.webLoginBlocked(ctx, lookup, ipHash)
	if err != nil {
		return 0, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	if blocked {
		return 0, httpx.NewError(httpx.ErrRateLimited, msgWebLoginLimited)
	}
	row, err := s.userByLogin(ctx, lookup)
	failed := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		verifyPassword(dummyHash, password)
		failed = true
	case err != nil:
		return 0, err
	case !verifyPassword(row.pwHash, password) || row.user.Status != "ACTIVE":
		failed = true
	}
	if failed {
		if err := s.noteWebLoginFailure(ctx, lookup, ipHash); err != nil {
			return 0, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
		}
		return 0, invalid
	}
	return row.user.ID, nil
}

func (s *Service) IssueWebSession(ctx context.Context, userID int64) (string, error) {
	if s == nil || s.Redis == nil || s.Clock == nil || userID <= 0 {
		return "", httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expires := s.Clock.Now().Add(webSessionTTL)
	raw, err := json.Marshal(webSessionRecord{UserID: userID, ExpiresAt: expires})
	if err != nil {
		return "", err
	}
	if err := s.Redis.SetEX(ctx, webSessionKey(token), string(raw), webSessionTTL); err != nil {
		return "", httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	return token, nil
}

func (s *Service) WebSessionUser(ctx context.Context, token string) (int64, error) {
	missing := httpx.NewError(httpx.ErrTokenMissing, msgWebLoginRequired)
	if s == nil || s.Redis == nil || s.Clock == nil || token == "" {
		return 0, missing
	}
	raw, err := s.Redis.Get(ctx, webSessionKey(token))
	if errors.Is(err, redisx.ErrMissing) {
		return 0, missing
	}
	if err != nil {
		return 0, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	var rec webSessionRecord
	if json.Unmarshal([]byte(raw), &rec) != nil || rec.UserID <= 0 {
		return 0, missing
	}
	if !s.Clock.Now().Before(rec.ExpiresAt) {
		_ = s.Redis.Del(ctx, webSessionKey(token))
		return 0, missing
	}
	return rec.UserID, nil
}

func (s *Service) RevokeWebSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if s == nil || s.Redis == nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试")
	}
	if err := s.Redis.Del(ctx, webSessionKey(token)); err != nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	return nil
}

func (s *Service) WebAccount(ctx context.Context, userID int64) (WebAccount, error) {
	var none WebAccount
	if s == nil || s.Repo == nil || s.Repo.Pool == nil || s.Clock == nil || userID <= 0 {
		return none, httpx.Internal
	}
	now := s.Clock.Now()
	u, err := s.Repo.userByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, httpx.NewError(httpx.ErrTokenMissing, msgWebLoginRequired)
		}
		return none, err
	}
	if u.Status != "ACTIVE" {
		return none, httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
	}
	address, err := s.loginAddress(ctx, userID)
	if err != nil {
		return none, err
	}
	ent, err := s.Repo.entitlement(ctx, userID)
	if err != nil {
		return none, err
	}
	count, err := s.Repo.countActiveDevices(ctx, s.Repo.Pool, userID)
	if err != nil {
		return none, err
	}
	limit, err := s.Repo.deviceLimit(ctx, s.Repo.Pool, userID, now)
	if err != nil {
		return none, err
	}
	name := ent.PlanName
	if name == "" {
		name = "未开通"
	}
	active := ent.Tier > 0 && ent.ExpireAt != nil && ent.ExpireAt.After(now)
	return WebAccount{
		Account: address, PlanName: name, ExpireAt: ent.ExpireAt, Active: active,
		DeviceCount: count, DeviceLimit: limit, ServerTime: now,
	}, nil
}

func writeWebSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearWebSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func webUserID(c *gin.Context) int64 {
	v, ok := c.Get(ctxWebUser)
	if !ok {
		return 0
	}
	id, _ := v.(int64)
	return id
}

func webUserKey(c *gin.Context) string {
	id := webUserID(c)
	if id <= 0 {
		return ""
	}
	return itoa64(id)
}

func (h *Handler) requireWebSession(c *gin.Context) {
	token, err := c.Cookie(webSessionCookieName)
	if err != nil || token == "" {
		httpx.Fail(c, httpx.NewError(httpx.ErrTokenMissing, msgWebLoginRequired))
		return
	}
	id, err := h.Svc.WebSessionUser(c.Request.Context(), token)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Set(ctxWebUser, id)
	c.Next()
}

func (h *Handler) webLogin(c *gin.Context) {
	var body webLoginBody
	if _, ok := readJSON(c, &body); !ok {
		return
	}
	if _, err := normalizeLoginAddress(body.Address); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.Svc.VerifyWebChallenge(c.Request.Context(), body.ChallengeID, body.Counter, auth.ClientIP(c)); err != nil {
		httpx.Fail(c, err)
		return
	}
	userID, err := h.Svc.LoginFromWeb(c.Request.Context(), body.Address, body.Password, auth.ClientIP(c))
	h.Svc.ConsumeWebChallenge(c.Request.Context(), body.ChallengeID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	token, err := h.Svc.IssueWebSession(c.Request.Context(), userID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeWebSessionCookie(c, token, int(webSessionTTL.Seconds()))
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"logged_in": true}, "request_id": httpx.RequestID(c)})
}

func (h *Handler) webLogout(c *gin.Context) {
	token, _ := c.Cookie(webSessionCookieName)
	if err := h.Svc.RevokeWebSession(c.Request.Context(), token); err != nil {
		httpx.Fail(c, err)
		return
	}
	clearWebSessionCookie(c)
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"logged_out": true}, "request_id": httpx.RequestID(c)})
}

func (h *Handler) webAccount(c *gin.Context) {
	out, err := h.Svc.WebAccount(c.Request.Context(), webUserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if h.Trials != nil {
		status, err := h.Trials.ViewForAccount(c.Request.Context(), webUserID(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		out.TrialEligible = status.Eligible && status.Enabled
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": out, "request_id": httpx.RequestID(c)})
}

func (h *Handler) webClaimTrial(c *gin.Context) {
	if h.Trials == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试"))
		return
	}
	raw, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		var extra struct{}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&extra); err != nil || dec.More() {
			httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
			return
		}
	}
	out, err := h.Trials.ClaimForAccount(c.Request.Context(), webUserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": out, "request_id": httpx.RequestID(c)})
}

func (h *Handler) webRedeem(c *gin.Context) {
	if h.Redeems == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试"))
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if _, ok := readJSON(c, &body); !ok {
		return
	}
	out, err := h.Redeems.Redeem(c.Request.Context(), webUserID(c), body.Code)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"data": out, "request_id": httpx.RequestID(c)})
}
