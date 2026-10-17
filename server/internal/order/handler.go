package order

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Handler struct {
	Svc *Service
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	user := func(c *gin.Context) string { return strconv.FormatInt(httpx.UserID(c), 10) }
	dev := func(c *gin.Context) string { return strconv.FormatInt(httpx.DeviceID(c), 10) }
	v1.POST("/orders", h.MW.S2(), h.MW.RateLimit("order_user", 10, time.Hour, true, user), h.create)
	v1.GET("/orders", h.MW.S1(), h.list)
	v1.GET("/orders/:order_no", h.MW.S1(), h.MW.RateLimit("order_poll_dev", 60, time.Minute, false, dev), h.get)
	v1.POST("/orders/:order_no/cancel", h.MW.S2(), h.cancel)
}

func (h *Handler) create(c *gin.Context) {
	idem := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(idem); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	var in CreateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	v, err := h.Svc.Create(c.Request.Context(), httpx.UserID(c), in, idem)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.Created(c, v)
}

func (h *Handler) get(c *gin.Context) {
	v, err := h.Svc.Get(c.Request.Context(), httpx.UserID(c), c.Param("order_no"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("ETag", v.ETag())
	if c.GetHeader("If-None-Match") == v.ETag() {
		c.Status(http.StatusNotModified)
		return
	}
	httpx.OK(c, v)
}

func (h *Handler) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	items, next, err := h.Svc.List(c.Request.Context(), httpx.UserID(c), c.Query("cursor"), limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var nextCursor *string
	if next != "" {
		nextCursor = &next
	}
	httpx.OK(c, gin.H{"items": items, "next_cursor": nextCursor})
}

func (h *Handler) cancel(c *gin.Context) {
	v, err := h.Svc.Cancel(c.Request.Context(), httpx.UserID(c), c.Param("order_no"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}
