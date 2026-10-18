package redeem

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
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

// 每个账号每小时 10 次、每个 IP 每小时 30 次。码有 80 位熵，这个额度只是拦住撞库。
const (
	redeemUserLimit   = 10
	redeemIPLimit     = 30
	redeemLimitWindow = time.Hour
)

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	user := func(c *gin.Context) string {
		id := httpx.UserID(c)
		if id <= 0 {
			return ""
		}
		return strconv.FormatInt(id, 10)
	}
	ip := func(c *gin.Context) string { return auth.ClientIP(c) }
	v1.POST("/redeem-codes",
		h.MW.S2(),
		h.MW.RateLimitMessage("redeem_user", redeemUserLimit, redeemLimitWindow, true, "尝试太频繁", user),
		h.MW.RateLimitMessage("redeem_ip", redeemIPLimit, redeemLimitWindow, true, "尝试太频繁", ip),
		h.redeem)
}

func (h *Handler) redeem(c *gin.Context) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeRedeem(c, &in); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.Svc.Redeem(c.Request.Context(), httpx.UserID(c), in.Code)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func decodeRedeem(c *gin.Context, out any) error {
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" {
		return httpx.NewError(httpx.ErrBadRequest, "请使用 JSON 格式")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	if err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "兑换码格式不正确")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil || dec.More() {
		return httpx.NewError(httpx.ErrBadRequest, "兑换码格式不正确")
	}
	return nil
}
