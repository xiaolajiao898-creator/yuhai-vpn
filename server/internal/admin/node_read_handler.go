package admin

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
	"net/http"
)

type NodeReadHandler struct {
	Auth    *Handler
	Service *NodeReadService
}

func (h *NodeReadHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/nodes", h.list)
	secured.GET("/nodes/:id", h.get)
}
func nodeReadFailure(c *gin.Context, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		err = httpx.NewError(httpx.ErrInternal, "节点资料暂不可用，请重新查询")
	}
	httpx.Fail(c, err)
}
func (h *NodeReadHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "search", "status", "country_code")
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
	out, err := h.Service.List(c.Request.Context(), p, ManagedNodeFilter{Page: page, PageSize: size, Search: q.Get("search"), Status: q.Get("status"), CountryCode: q.Get("country_code")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *NodeReadHandler) get(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Get(c.Request.Context(), p, id)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
