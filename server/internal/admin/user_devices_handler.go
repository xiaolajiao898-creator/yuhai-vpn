package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type UserDevicesHandler struct {
	Auth    *Handler
	Service *UserReadService
}

func (h *UserDevicesHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/devices", h.overview)
	secured.GET("/users/:id/devices", h.list)
	secured.GET("/users/:id/devices/:device_id", h.get)
	secured.GET("/users/:id/devices/:device_id/receipts", h.receipts)
}

func (h *UserDevicesHandler) get(c *gin.Context) {
	userID, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	deviceID, err := positiveApprovalID(c.Param("device_id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	if _, err = approvalQuery(c); err != nil {
		userReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Device(c.Request.Context(), p, userID, deviceID)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *UserDevicesHandler) list(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	q, err := approvalQuery(c, "page", "page_size", "status")
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
	out, err := h.Service.Devices(c.Request.Context(), p, id, ManagedDeviceFilter{Page: page, PageSize: size, Status: q.Get("status")})
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// receipts 只读，不接受任何查询参数；确认与否的语义见 DeviceRevocationReceipt 的注释。
func (h *UserDevicesHandler) receipts(c *gin.Context) {
	userID, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	deviceID, err := positiveApprovalID(c.Param("device_id"))
	if err != nil {
		userReadFailure(c, err)
		return
	}
	if _, err = approvalQuery(c); err != nil {
		userReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Receipts(c.Request.Context(), p, userID, deviceID)
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// overview 跨用户列出设备。与按用户查那份共用 users.read 与同一套投影，
// 区别只是不先选定用户；查询参数白名单化，打错的筛选条件会被拒绝而不是静默忽略。
func (h *UserDevicesHandler) overview(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status", "platform", "user_id")
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
	out, err := h.Service.Overview(c.Request.Context(), p, ManagedDeviceOverviewFilter{Page: page,
		PageSize: size, Status: q.Get("status"), Platform: q.Get("platform"), UserID: q.Get("user_id")})
	if err != nil {
		userReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
