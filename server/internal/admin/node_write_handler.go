package admin

import (
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
)

type NodeWriteHandler struct {
	Auth    *Handler
	Service *NodeWriteService
	Read    *NodeReadService
}

func (h *NodeWriteHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/node-groups", h.listGroups)
	secured.POST("/node-groups", h.createGroup)
	secured.POST("/nodes", h.createNode)
	secured.PATCH("/nodes/:id", h.updateNode)
	secured.GET("/nodes/:id/inbounds", h.listInbounds)
	secured.POST("/nodes/:id/inbounds", h.createInbound)
}

// nodeWriteBody 严格读取请求体：必须是 application/json、无查询串、不超过 8KB，
// 且不接受未知字段与重复键（decodeApprovalObject）。返回原始字节用于计算 request_hash。
func nodeWriteBody(c *gin.Context, out any) ([]byte, error) {
	bad := badNodeInput("请求体必须是不超过 8KB 的 JSON 对象，且不含未知字段")
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" || c.Request.URL.RawQuery != "" {
		return nil, bad
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024))
	if err != nil || decodeApprovalObject(raw, out) != nil {
		return nil, bad
	}
	return raw, nil
}

func (h *NodeWriteHandler) createGroup(c *gin.Context) {
	var in NodeGroupCreate
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.CreateGroup(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (h *NodeWriteHandler) createNode(c *gin.Context) {
	var in NodeCreate
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.CreateNode(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (h *NodeWriteHandler) updateNode(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in NodeUpdate
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.UpdateNode(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

func (h *NodeWriteHandler) createInbound(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in InboundCreate
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.CreateInbound(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (h *NodeWriteHandler) listGroups(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Read.Groups(c.Request.Context(), p)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *NodeWriteHandler) listInbounds(c *gin.Context) {
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
	out, err := h.Read.Inbounds(c.Request.Context(), p, id)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ---------- L2：节点身份（注册令牌 / 吊销） ----------

type NodeEnrollHandler struct {
	Auth    *Handler
	Service *NodeEnrollService
}

func (h *NodeEnrollHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.POST("/nodes/:id/enroll-tokens", h.issue)
	secured.POST("/nodes/:id/revoke", h.revoke)
}

func (h *NodeEnrollHandler) issue(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in NodeSecurityInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.IssueEnrollToken(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	// 令牌只在这一次响应里出现，绝不缓存。
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *NodeEnrollHandler) revoke(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in NodeSecurityInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	if err := h.Service.RevokeNode(c.Request.Context(), approvalProof(c, raw), id, in); err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}
