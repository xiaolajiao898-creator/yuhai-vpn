package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type DashboardHandler struct {
	Auth    *Handler
	Service *DashboardService
}

func (h *DashboardHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/dashboard", h.get)
}

// get 不接受任何查询参数：总览的口径固定，可调的区间统计走 GET /traffic/stats。
func (h *DashboardHandler) get(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Get(c.Request.Context(), p)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
