package admin

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

type AdminManagementHandler struct {
	Auth    *Handler
	Service *AdminManagementService
}

func (h *AdminManagementHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/admins", h.list)
	secured.POST("/admins", h.create)
	secured.PUT("/admins/:id/roles", h.roles)
}
func (h *AdminManagementHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "username", "status")
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
	out, err := h.Service.List(c.Request.Context(), p, ManagedAdminFilter{Page: page, PageSize: size, Username: q.Get("username"), Status: q.Get("status")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}
func (h *AdminManagementHandler) create(c *gin.Context) {
	var in AdminCreateInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.RequestCreate(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, gin.H{"approval_id": out.ID})
}
func (h *AdminManagementHandler) roles(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in AdminRolesInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.RequestRoles(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, gin.H{"approval_id": out.ID})
}
