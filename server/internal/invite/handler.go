package invite

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Reader interface {
	Summary(context.Context, int64) (Info, error)
	Rewards(context.Context, int64, int, int64) (RewardPage, error)
}

type Handler struct {
	Svc    Reader
	MW     *auth.Middleware
	Binder *Binder
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	v1.GET("/invite", h.MW.S1(), h.summary)
	v1.GET("/invite/rewards", h.MW.S1(), h.rewards)
	v1.POST("/invite/bind", h.MW.S2(), h.MW.RateLimit("invite_bind_user", 5, 24*time.Hour, true, func(c *gin.Context) string { return strconv.FormatInt(httpx.UserID(c), 10) }), h.bind)
}

func (h *Handler) summary(c *gin.Context) {
	userID := httpx.UserID(c)
	if userID <= 0 {
		httpx.Fail(c, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份"))
		return
	}
	info, err := h.Svc.Summary(c.Request.Context(), userID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, info)
}

func (h *Handler) rewards(c *gin.Context) {
	userID := httpx.UserID(c)
	if userID <= 0 {
		httpx.Fail(c, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份"))
		return
	}
	limit, before, err := rewardParams(c.Request.URL.Query(), userID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	page, err := h.Svc.Rewards(c.Request.Context(), userID, limit, before)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, page)
}

func badPagination() error { return httpx.NewError(httpx.ErrBadRequest, "分页参数无效") }

func rewardParams(q url.Values, userID int64) (limit int, before int64, err error) {
	limit = 20
	if values, exists := q["limit"]; exists {
		if len(values) != 1 {
			return 0, 0, badPagination()
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, badPagination()
		}
	}
	if values, exists := q["cursor"]; exists {
		if len(values) != 1 || len(values[0]) > 128 {
			return 0, 0, badPagination()
		}
		raw, decodeErr := base64.RawURLEncoding.DecodeString(values[0])
		parts := strings.Split(string(raw), ":")
		if decodeErr != nil || len(parts) != 4 || parts[0] != "v1" || parts[1] != "invite-rewards" || parts[2] != strconv.FormatInt(userID, 10) {
			return 0, 0, badPagination()
		}
		before, err = strconv.ParseInt(parts[3], 10, 64)
		if err != nil || before <= 0 {
			return 0, 0, badPagination()
		}
	}
	return limit, before, nil
}

// A cursor is only a position within this authenticated user's rows. Even a
// modified cursor cannot change the beneficiary filter in Service.Rewards.
func rewardCursor(userID int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:invite-rewards:" + strconv.FormatInt(userID, 10) + ":" + id))
}
