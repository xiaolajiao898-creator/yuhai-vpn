package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type FinanceAddressHandler struct {
	Auth   *Handler
	Read   *FinanceReadService
	Write  *FinanceAddressService
	Import *FinanceImportService
}

func (h *FinanceAddressHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/shared-pay-addresses", h.listShared)
	secured.POST("/shared-pay-addresses", h.addShared)
	secured.POST("/receive-addresses", h.create)
	secured.POST("/receive-addresses/:id/status", h.setStatus)
	secured.POST("/receive-addresses/import", h.importPackage)
}

func (h *FinanceAddressHandler) listShared(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size")
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
	out, err := h.Read.SharedPayAddresses(c.Request.Context(), p, FinanceFilter{Page: page, PageSize: size})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceAddressHandler) addShared(c *gin.Context) {
	var in SharedPayAddressInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.AddSharedPayAddress(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *FinanceAddressHandler) create(c *gin.Context) {
	var in CreateReceiveAddressInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.CreateReceiveAddress(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *FinanceAddressHandler) setStatus(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in ReceiveAddressStatusInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.SetReceiveAddressStatus(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// importPackage 发起地址包导入审批。返回 202 与审批单编号——**这时地址还没入库**，
// 要等另一名管理员在审批中心点通过。
func (h *FinanceAddressHandler) importPackage(c *gin.Context) {
	var in AddressImportInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Import.RequestImport(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, gin.H{"approval_id": out.ID})
}
