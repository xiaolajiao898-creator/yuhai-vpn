package worldvpn

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// Handler 把批发线路接到 App 已有的登录态上。未配置时返回 50303，不访问 WorldVPN。
type Handler struct {
	Svc *Service
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	dev := func(c *gin.Context) string { return strconv.FormatInt(httpx.DeviceID(c), 10) }
	v1.GET("/worldvpn/lines", h.MW.S1(), h.MW.RateLimit("worldvpn_lines_dev", 20, time.Minute, false, dev), h.lines)
	v1.POST("/connect/worldvpn", h.MW.S2(), h.MW.RateLimit("worldvpn_connect_dev", 30, time.Minute, false, dev), h.connect)
	v1.POST("/connect/worldvpn/release", h.MW.S2(), h.MW.RateLimit("worldvpn_release_dev", 30, time.Minute, false, dev), h.release)
}

func (h *Handler) lines(c *gin.Context) {
	if h.Svc == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "批发线路未配置"))
		return
	}
	items, err := h.Svc.Lines(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-cache")
	httpx.OK(c, gin.H{"lines": items})
}

type connectInput struct {
	LineID string `json:"line_id"`
}

func (h *Handler) connect(c *gin.Context) {
	if h.Svc == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "批发线路未配置"))
		return
	}
	var in connectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	out, err := h.Svc.Connect(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), in.LineID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, out)
}

func (h *Handler) release(c *gin.Context) {
	if h.Svc == nil {
		httpx.OK(c, gin.H{"released": false})
		return
	}
	if err := h.Svc.Release(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c)); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, gin.H{"released": true})
}
