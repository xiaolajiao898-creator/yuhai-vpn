package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type PlanHandler struct {
	Auth  *Handler
	Read  *PlanReadService
	Write *PlanWriteService
}

func (h *PlanHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/plans", h.list)
	secured.POST("/plans", h.create)
	secured.PATCH("/plans/:id", h.update)
	secured.POST("/plans/:id/status", h.status)
}

func (h *PlanHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "status")
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Read.List(c.Request.Context(), p, q.Get("status"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *PlanHandler) create(c *gin.Context) {
	var in PlanCreate
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.Create(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *PlanHandler) update(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in PlanPatch
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	// 原始请求体要继续往下传：只有它能区分「没提这个字段」与「显式给了 null」。
	out, err := h.Write.Update(c.Request.Context(), approvalProof(c, raw), id, in, raw)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *PlanHandler) status(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in PlanStatusInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	if err := h.Write.SetStatus(c.Request.Context(), approvalProof(c, raw), id, in); err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}
