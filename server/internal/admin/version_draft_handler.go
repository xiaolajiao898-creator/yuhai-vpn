package admin

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type VersionHandler struct {
	Auth    *Handler
	Service *VersionService
}

func (h *VersionHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/app-versions", h.list)
	secured.GET("/app-versions/:id", h.get)
	secured.POST("/app-versions", h.create)
	secured.PUT("/app-versions/:id", h.update)
}
func (h *VersionHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "platform", "status", "version_code")
	if err != nil {
		approvalFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.List(c.Request.Context(), p, VersionFilter{Page: page, PageSize: size, Platform: q.Get("platform"), Status: q.Get("status"), VersionCode: q.Get("version_code")})
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *VersionHandler) get(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		approvalFailure(c, err)
		return
	}
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		approvalFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Get(c.Request.Context(), p, id)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func versionDraftBody(c *gin.Context, out any, update bool) ([]byte, error) {
	bad := httpx.NewError(httpx.ErrBadRequest, "草稿请求必须包含全部字段且不超过24KB")
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" || c.Request.URL.RawQuery != "" {
		return nil, bad
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 24*1024))
	if err != nil || decodeApprovalObject(raw, out) != nil {
		return nil, bad
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, bad
	}
	required := []string{"operation_id", "platform", "version_name", "version_code", "min_supported_code", "download_url", "file_sha256", "cert_sha256", "release_notes"}
	if update {
		required = append(required, "expected_snapshot_hash")
	}
	for _, key := range required {
		value, ok := fields[key]
		if !ok {
			return nil, bad
		}
		// Only the certificate may explicitly be null. Other required scalars
		// must not silently decode null into an empty string.
		if key != "cert_sha256" {
			var text *string
			if json.Unmarshal(value, &text) != nil || text == nil {
				return nil, bad
			}
		}
	}
	return raw, nil
}
func (h *VersionHandler) invalid(c *gin.Context, id int64, err error) {
	e := h.Service.Approvals.auditEvent(approvalProof(c, nil), versionDraft, "app_version", id)
	e.UserAgent = ""
	approvalFailure(c, h.Service.Approvals.RecordFailure(c.Request.Context(), e, err))
}
func (h *VersionHandler) create(c *gin.Context) {
	var in VersionCreate
	raw, err := versionDraftBody(c, &in, false)
	if err != nil {
		h.invalid(c, 0, err)
		return
	}
	out, err := h.Service.Save(c.Request.Context(), approvalProof(c, raw), 0, VersionUpdate{VersionCreate: in})
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}
func (h *VersionHandler) update(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		h.invalid(c, 0, err)
		return
	}
	var in VersionUpdate
	raw, err := versionDraftBody(c, &in, true)
	if err != nil {
		h.invalid(c, id, err)
		return
	}
	out, err := h.Service.Save(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
