package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type TrafficReadHandler struct {
	Auth    *Handler
	Service *TrafficReadService
}

func (h *TrafficReadHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/traffic/stats", h.stats)
}

func (h *TrafficReadHandler) stats(c *gin.Context) {
	// approvalQuery 只放行这里列出的查询参数：多传一个未知参数会被拒绝，
	// 免得「打错的筛选条件被静默忽略、看板却照常出数」。
	q, err := approvalQuery(c, "dim", "from", "to", "page", "page_size")
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Stats(c.Request.Context(), p, TrafficFilter{Dim: q.Get("dim"),
		From: q.Get("from"), To: q.Get("to"), Page: page, PageSize: size})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
