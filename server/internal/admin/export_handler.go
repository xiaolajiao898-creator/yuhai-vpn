package admin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type ExportHandler struct {
	Auth    *Handler
	Service *ExportService
}

func (h *ExportHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/exports", h.list)
	secured.POST("/exports", h.create)
	secured.POST("/exports/:id/download", h.download)
}

func (h *ExportHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "kind", "status")
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
	out, err := h.Service.Exports(c.Request.Context(), p,
		ExportFilter{Page: page, PageSize: size, Kind: q.Get("kind"), Status: q.Get("status")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// create 登记导出任务，返回 202——**文件这时还不存在**，生成由 worker 异步完成。
func (h *ExportHandler) create(c *gin.Context) {
	var in ExportCreateInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Service.RequestExport(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, out)
}

// download 返回 CSV 原文，不套 JSON 信封。失败仍然按统一错误信封返回——
// 出错时给调用方一份「看起来像文件」的响应，只会让人把错误信息当成数据存下来。
func (h *ExportHandler) download(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in ExportDownloadInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	file, err := h.Service.Download(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Export-Sha256", file.SHA256)
	c.Header("Content-Disposition", `attachment; filename="`+file.Name+`"`)
	c.Header("Content-Length", strconv.Itoa(len(file.Content)))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", file.Content)
}
