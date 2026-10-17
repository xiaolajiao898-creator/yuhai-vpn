package traffic

import (
	"context"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Reader interface {
	Daily(context.Context, int64, int) (DailyUsage, error)
}

type Handler struct {
	Svc Reader
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	v1.GET("/traffic/daily", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}, h.MW.S1(), h.daily)
}

func (h *Handler) daily(c *gin.Context) {
	userID := httpx.UserID(c)
	if userID <= 0 {
		httpx.Fail(c, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份"))
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		httpx.Fail(c, badDays())
		return
	}
	days, err := parseDays(query)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	result, err := h.Svc.Daily(c.Request.Context(), userID, days)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, result)
}

func badDays() error { return httpx.NewError(httpx.ErrBadRequest, "days 必须是 1 至 90 的整数") }

func parseDays(query url.Values) (int, error) {
	values, exists := query["days"]
	if !exists {
		return defaultDays, nil
	}
	if len(values) != 1 {
		return 0, badDays()
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < 1 || n > maxDays || strconv.Itoa(n) != values[0] {
		return 0, badDays()
	}
	return n, nil
}
