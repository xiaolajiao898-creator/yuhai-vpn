package trial

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

const (
	claimUserLimit = 5
	claimIPLimit   = 20
	claimWindow    = time.Hour
)

type Handler struct {
	Svc *Service
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	user := func(c *gin.Context) string {
		id := httpx.UserID(c)
		if id <= 0 {
			return ""
		}
		return strconv.FormatInt(id, 10)
	}
	ip := func(c *gin.Context) string { return auth.ClientIP(c) }
	v1.GET("/trial", h.MW.S1(), h.view)
	v1.POST("/trial",
		h.MW.S2(),
		h.MW.RateLimitMessage("trial_user", claimUserLimit, claimWindow, true, "尝试太频繁", user),
		h.MW.RateLimitMessage("trial_ip", claimIPLimit, claimWindow, true, "尝试太频繁", ip),
		h.claim)
}

func (h *Handler) view(c *gin.Context) {
	out, err := h.Svc.View(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h *Handler) claim(c *gin.Context) {
	var in struct {
		InstallID string `json:"install_id"`
	}
	if err := decodeClaim(c, &in); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.Svc.Claim(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), in.InstallID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func decodeClaim(c *gin.Context, out any) error {
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" {
		return httpx.NewError(httpx.ErrBadRequest, "请使用 JSON 格式")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
	if err != nil {
		return ErrBadInstall
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil || dec.More() {
		return ErrBadInstall
	}
	return nil
}
