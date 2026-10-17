package admin

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type DeviceRevokeHandler struct {
	Auth    *Handler
	Service *DeviceRevokeService
}

func (h *DeviceRevokeHandler) Register(g *gin.RouterGroup) {
	g.Group("", h.Auth.Middleware()).DELETE("/devices/:id", h.revoke)
}
func deviceRevokeFailure(c *gin.Context, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		err = httpx.NewError(httpx.ErrInternal, "设备撤销或审计服务暂不可用，请只读核实设备状态")
	}
	httpx.Fail(c, err)
}
func (h *DeviceRevokeHandler) revoke(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	var in DeviceRevokeInput
	var raw []byte
	if err == nil {
		raw, err = approvalBody(c, &in)
	}
	if err != nil {
		e := h.Service.Security.auditEvent(approvalProof(c, nil), deviceRevokeAction, "device", id)
		e.UserAgent = ""
		deviceRevokeFailure(c, h.Service.Security.RecordFailure(c.Request.Context(), e, err))
		return
	}
	out, err := h.Service.Revoke(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		deviceRevokeFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
