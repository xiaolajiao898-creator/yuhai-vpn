package admin

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const CookieName = "yh_admin"
const principalKey = "yh.admin.principal"

type Handler struct {
	Service       *Service
	SecureCookies bool
}

func (h *Handler) Register(g *gin.RouterGroup) {
	g.Use(h.SecurityHeaders())
	g.POST("/auth/login", h.Login)
	g.POST("/auth/mfa", h.MFA)
	g.POST("/step-up", h.Middleware(), h.StepUp)
	session := g.Group("/auth", h.Middleware())
	session.GET("/me", h.Me)
	session.POST("/logout", h.Logout)
}
func (h *Handler) SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(withAuthRequest(c.Request.Context(), httpx.RequestID(c), c.ClientIP()))
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Frame-Options", "DENY")
		if !h.sameOrigin(c.Request) {
			h.authGuardFailure(c, httpx.NewError(httpx.ErrForbidden, "请求来源不被允许"))
			return
		}
		c.Next()
	}
}

// Origin is checked against the actual request host. Do not trust arbitrary
// X-Forwarded-Host; configure a trusted reverse proxy and fixed Host upstream.
func (h *Handler) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	expected := "http"
	if h.SecureCookies || r.TLS != nil {
		expected = "https"
	}
	return u.Scheme == expected
}
func decode(c *gin.Context, out any) error {
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" {
		return httpx.NewError(httpx.ErrBadRequest, "请使用 JSON 格式")
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "参数格式错误或过长")
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return httpx.NewError(httpx.ErrBadRequest, "参数格式错误")
	}
	return nil
}
func (h *Handler) Login(c *gin.Context) {
	var in LoginInput
	if err := decode(c, &in); err != nil {
		h.authGuardFailure(c, err)
		return
	}
	out, err := h.Service.Login(c.Request.Context(), in, c.ClientIP())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) StepUp(c *gin.Context) {
	p, ok := Current(c)
	if !ok {
		h.authGuardFailure(c, errSession)
		return
	}
	var in StepUpInput
	if err := decode(c, &in); err != nil {
		h.authGuardFailure(c, err)
		return
	}
	out, err := h.Service.IssueStepUp(c.Request.Context(), p, in, c.ClientIP())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *Handler) MFA(c *gin.Context) {
	var in MFAInput
	if err := decode(c, &in); err != nil {
		h.authGuardFailure(c, err)
		return
	}
	out, err := h.Service.MFA(c.Request.Context(), in, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: CookieName, Value: out.Token, Path: "/admin/v1", HttpOnly: true, Secure: h.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds()), Expires: h.Service.Clock.Now().Add(sessionTTL)})
	c.JSON(http.StatusOK, out)
}
func (h *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(withAuthRequest(c.Request.Context(), httpx.RequestID(c), c.ClientIP()))
		c.Header("Cache-Control", "no-store")
		if !h.sameOrigin(c.Request) {
			h.authGuardFailure(c, httpx.NewError(httpx.ErrForbidden, "请求来源不被允许"))
			return
		}
		token, err := c.Cookie(CookieName)
		if err != nil || !validToken(token) {
			h.authGuardFailure(c, errSession)
			return
		}
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" && c.Request.Method != "OPTIONS" && !validCSRF(token, c.GetHeader("X-CSRF-Token")) {
			h.authGuardFailure(c, httpx.NewError(httpx.ErrForbidden, "安全令牌无效，请刷新后重试"))
			return
		}
		p, err := h.Service.Authenticate(c.Request.Context(), token, c.ClientIP())
		if err != nil {
			h.authGuardFailure(c, err)
			return
		}
		c.Set(principalKey, p)
		c.Next()
	}
}
func Current(c *gin.Context) (Principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return Principal{}, false
	}
	p, ok := v.(Principal)
	return p, ok
}
func RequirePermission(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := Current(c)
		if !ok || !p.HasPermission(code) {
			httpx.Fail(c, httpx.NewError(httpx.ErrForbidden, "权限不足"))
			return
		}
		c.Next()
	}
}
func (h *Handler) Me(c *gin.Context) {
	p, ok := Current(c)
	if !ok {
		h.authGuardFailure(c, errSession)
		return
	}
	c.JSON(http.StatusOK, p)
}
func (h *Handler) Logout(c *gin.Context) {
	p, ok := Current(c)
	if !ok {
		h.authGuardFailure(c, errSession)
		return
	}
	if err := h.Service.Logout(c.Request.Context(), p.SessionID); err != nil {
		httpx.Fail(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: CookieName, Value: "", Path: "/admin/v1", HttpOnly: true, Secure: h.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC()})
	c.Status(http.StatusNoContent)
}

// Guards have not called a credential service yet, so they own their denial
// audit. Restrict this to the four authentication writes, never read traffic.
func (h *Handler) authGuardFailure(c *gin.Context, cause error) {
	action := ""
	if c.Request.Method == http.MethodPost {
		switch c.FullPath() {
		case "/admin/v1/auth/login":
			action = auditLogin
		case "/admin/v1/auth/mfa":
			action = auditMFA
		case "/admin/v1/auth/logout":
			action = auditLogout
		case "/admin/v1/step-up":
			action = auditStepUp
		}
	}
	if action != "" && h.Service != nil && h.Service.Pool != nil {
		if err := h.Service.limitLogin(c.Request.Context(), c.ClientIP()); err != nil {
			cause = err
		}
		// No account identity is inferred from unverified credentials/headers.
		cause = h.Service.authFailure(c.Request.Context(), authEvent(c.Request.Context(), action, c.ClientIP()), cause)
	}
	httpx.Fail(c, safeAuthError(cause))
}
