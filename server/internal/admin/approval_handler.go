package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type ApprovalHandler struct {
	Auth    *Handler
	Service *ApprovalService
	Audit   *audit.Service
}

func (h *ApprovalHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/approvals", h.list)
	secured.GET("/approvals/:id", h.get)
	secured.POST("/approvals/:id/approve", h.approve)
	secured.POST("/approvals/:id/reject", h.reject)
	secured.POST("/app-versions/:id/publish", h.publish)
	secured.GET("/audit-logs", h.auditList)
	secured.POST("/audit-logs/verify", h.auditVerify)
}
func decodeApprovalObject(raw []byte, out any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid JSON encoding")
	}
	// Detect duplicate top-level keys as well as unknown keys. Approval inputs
	// have only scalar fields; permissive last-value-wins parsing is unnecessary.
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("JSON object required")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return errors.New("duplicate or invalid JSON key")
		}
		seen[key] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
	}
	if last, err := d.Token(); err != nil || last != json.Delim('}') {
		return errors.New("invalid JSON object")
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func approvalBody(c *gin.Context, out any) ([]byte, error) {
	bad := httpx.NewError(httpx.ErrBadRequest, "请求必须是有效且不超过4KB的JSON对象")
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" || c.Request.URL.RawQuery != "" {
		return nil, bad
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	if err != nil {
		return nil, bad
	}
	if err = decodeApprovalObject(raw, out); err != nil {
		return nil, bad
	}
	return raw, nil
}
func approvalQuery(c *gin.Context, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, httpx.NewError(httpx.ErrBadRequest, "查询参数无效")
	}
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key, values := range q {
		if !set[key] || len(values) != 1 || values[0] == "" {
			return nil, httpx.NewError(httpx.ErrBadRequest, "查询参数无效")
		}
	}
	return q, nil
}
func approvalPage(q url.Values) (int, int, error) {
	page, size := 1, 20
	for key, dst := range map[string]*int{"page": &page, "page_size": &size} {
		if value := q.Get(key); value != "" {
			n, err := positiveApprovalID(value)
			if err != nil || n > 1000000 {
				return 0, 0, httpx.NewError(httpx.ErrBadRequest, "分页参数无效")
			}
			*dst = int(n)
		}
	}
	if size > 50 {
		return 0, 0, httpx.NewError(httpx.ErrBadRequest, "每页最多50条")
	}
	return page, size, nil
}
func approvalFailure(c *gin.Context, err error) {
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		err = httpx.NewError(httpx.ErrInternal, "审批或审计服务暂不可用，请刷新核实")
	}
	httpx.Fail(c, err)
}
func approvalProof(c *gin.Context, raw []byte) ApprovalProof {
	p, _ := Current(c)
	return ApprovalProof{Principal: p, Token: c.GetHeader("X-Step-Up-Token"), RequestHash: RequestHash(c.Request.Method, c.Request.URL.RequestURI(), raw), RequestID: httpx.RequestID(c), IP: c.ClientIP(), UserAgent: c.Request.UserAgent()}
}
func (h *ApprovalHandler) invalidWrite(c *gin.Context, action, target string, id int64, err error) {
	e := h.Service.auditEvent(approvalProof(c, nil), action, target, id)
	if target == "approval" && id > 0 {
		e.ApprovalID = &id
	}
	approvalFailure(c, h.Service.RecordFailure(c.Request.Context(), e, err))
}
func (h *ApprovalHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "scope", "status")
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
	out, err := h.Service.List(c.Request.Context(), p, ApprovalFilter{Page: page, PageSize: size, Scope: q.Get("scope"), Status: q.Get("status")})
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *ApprovalHandler) get(c *gin.Context) {
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
func (h *ApprovalHandler) publish(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		h.invalidWrite(c, publishAction, "app_version", 0, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	raw, err := approvalBody(c, &in)
	if err != nil {
		h.invalidWrite(c, publishAction, "app_version", id, err)
		return
	}
	out, err := h.Service.RequestPublish(c.Request.Context(), approvalProof(c, raw), id, in.Reason)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusAccepted, out)
}
func (h *ApprovalHandler) approve(c *gin.Context) { h.decide(c, true) }
func (h *ApprovalHandler) reject(c *gin.Context)  { h.decide(c, false) }
func (h *ApprovalHandler) decide(c *gin.Context, approve bool) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		h.invalidWrite(c, approvalDecision, "approval", 0, err)
		return
	}
	var raw []byte
	var note string
	if approve {
		raw, err = approvalBody(c, &struct{}{})
	} else {
		var in struct {
			Note string `json:"note"`
		}
		raw, err = approvalBody(c, &in)
		note = in.Note
	}
	if err != nil {
		h.invalidWrite(c, approvalDecision, "approval", id, err)
		return
	}
	out, err := h.Service.Decide(c.Request.Context(), approvalProof(c, raw), id, approve, note)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *ApprovalHandler) auditList(c *gin.Context) {
	p, _ := Current(c)
	if err := h.Service.readAuthority(c.Request.Context(), p, "audit.read"); err != nil {
		approvalFailure(c, err)
		return
	}
	q, err := approvalQuery(c, "page", "page_size", "action", "result")
	if err != nil {
		approvalFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	out, err := h.Audit.List(c.Request.Context(), page, size, q.Get("action"), q.Get("result"))
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
func (h *ApprovalHandler) auditVerify(c *gin.Context) {
	p, _ := Current(c)
	if err := h.Service.readAuthority(c.Request.Context(), p, "audit.read"); err != nil {
		approvalFailure(c, err)
		return
	}
	var in struct {
		From string `json:"from_id"`
		To   string `json:"to_id"`
	}
	if _, err := approvalBody(c, &in); err != nil {
		approvalFailure(c, err)
		return
	}
	from, err := positiveApprovalID(in.From)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	to, err := positiveApprovalID(in.To)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	out, err := h.Audit.Verify(c.Request.Context(), from, to)
	if err != nil {
		approvalFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
