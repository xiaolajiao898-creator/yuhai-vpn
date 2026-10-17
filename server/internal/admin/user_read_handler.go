package admin

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
	"net/http"
)

type UserReadHandler struct {
	Auth    *Handler
	Service *UserReadService
}

func (h *UserReadHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/users", h.list)
	secured.GET("/users/:id", h.get)
	secured.GET("/users/:id/ledgers", h.ledgers)
}
func (h *UserReadHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "account_no", "status")
	if err != nil {
		userReadFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.List(c.Request.Context(), p, ManagedUserFilter{Page: page, PageSize: size, AccountNo: q.Get("account_no"), Status: q.Get("status")})
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *UserReadHandler) get(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		userReadFailure(c, err)
		return
	}
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Get(c.Request.Context(), p, id)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *UserReadHandler) ledgers(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	q, err := approvalQuery(c, "type", "page", "page_size")
	if err != nil {
		userReadFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Ledgers(c.Request.Context(), p, id, q.Get("type"), page, size)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func userReadFailure(c *gin.Context, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		err = httpx.NewError(httpx.ErrInternal, "用户数据暂不可用，请稍后重试")
	}
	httpx.Fail(c, err)
}
