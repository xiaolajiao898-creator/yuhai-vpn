package plan

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

type Handler struct {
	Svc *Service
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	user := func(c *gin.Context) string { return itoa(httpx.UserID(c)) }
	v1.GET("/plans", h.MW.S1(), h.list)
	v1.POST("/plans/quote", h.MW.S2(), h.MW.RateLimit("quote_user", 60, time.Hour, false, user), h.quote)
}

func (h *Handler) list(c *gin.Context) {
	plans, err := h.Svc.List(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"plans": plans,
		"topup": gin.H{"min": money.Micro(1 * money.OneUSDT).String(), "max": money.Micro(1000 * money.OneUSDT).String()},
	})
}

func (h *Handler) quote(c *gin.Context) {
	var in struct {
		PlanCode string `json:"plan_code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.PlanCode == "" {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	q, err := h.Svc.Quote(c.Request.Context(), httpx.UserID(c), in.PlanCode)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, q)
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
