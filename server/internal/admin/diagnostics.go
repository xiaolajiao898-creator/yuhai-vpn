package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/clientdiag"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// DiagnosticsHandler 让后台查看客户端连接失败时上报的诊断。
type DiagnosticsHandler struct {
	Auth  *Handler
	Store *clientdiag.PG
}

func (h *DiagnosticsHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware(), RequirePermission("users.diagnostics"))
	secured.GET("/connect-diagnostics", h.list)
}

func (h *DiagnosticsHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "user_id", "device_id", "page", "page_size")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	userID, err := optionalID(q.Get("user_id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	deviceID, err := optionalID(q.Get("device_id"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items, total, err := h.Store.List(c.Request.Context(), userID, deviceID, size, (page-1)*size)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": strconv.FormatInt(total, 10)})
}

func optionalID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, httpx.NewError(httpx.ErrBadRequest, "查询参数无效")
	}
	return n, nil
}
