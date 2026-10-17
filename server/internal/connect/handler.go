package connect

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Handler struct {
	Svc *Service
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	dev := func(c *gin.Context) string { return strconv.FormatInt(httpx.DeviceID(c), 10) }
	// S2：连接材料必须绑定到具体设备，不能只凭账户令牌取走。
	v1.POST("/connect/profiles", h.MW.S2(), h.MW.RateLimit("connect_dev", 30, time.Minute, false, dev), h.profiles)
	v1.GET("/egress", h.MW.S1(), h.MW.RateLimit("egress_list_dev", 20, time.Minute, false, dev), h.egressList)
	v1.POST("/connect/egress", h.MW.S2(), h.MW.RateLimit("connect_egress_dev", 30, time.Minute, false, dev), h.egressConnect)
}

func (h *Handler) egressList(c *gin.Context) {
	items, err := h.Svc.EgressList(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-cache")
	httpx.OK(c, gin.H{"egress": items})
}

type egressConnectInput struct {
	EgressID string `json:"egress_id"`
}

func (h *Handler) egressConnect(c *gin.Context) {
	var in egressConnectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	out, err := h.Svc.EgressConnect(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), in.EgressID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// 含第三方凭据，绝不允许任何中间层缓存。
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, out)
}

func (h *Handler) profiles(c *gin.Context) {
	var in Input
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	out, err := h.Svc.Profiles(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// 连接材料绝不允许被中间层缓存。
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, out)
}
