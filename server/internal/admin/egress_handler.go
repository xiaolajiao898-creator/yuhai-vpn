package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
)

type EgressHandler struct {
	Auth    *Handler
	Service *EgressService
}

func (h *EgressHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/egress", h.list)
	secured.POST("/egress/import/preview", h.preview)
	secured.POST("/egress/import", h.importNow)
	secured.PATCH("/egress/:id", h.update)
	secured.GET("/egress-subscriptions", h.subscriptions)
	secured.POST("/egress-subscriptions", h.createSubscription)
	secured.POST("/egress-subscriptions/preview", h.previewSubscription)
	secured.POST("/egress-subscriptions/:id/refresh", h.refreshSubscription)
}

// egressBody 严格读取：application/json、无查询串、限大小、不接受未知字段。
// 导入正文可达 1MB（外购代理常一次上千行），其余接口 8KB。
func egressBody(c *gin.Context, out any, max int64) ([]byte, error) {
	bad := badEgress("请求体必须是 JSON 对象，且不含未知字段、不超过大小限制")
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" || c.Request.URL.RawQuery != "" {
		return nil, bad
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, max))
	if err != nil {
		return nil, bad
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil || dec.More() {
		return nil, bad
	}
	return raw, nil
}

const importBodyMax = 1<<20 + 16<<10

func egressFailure(c *gin.Context, err error) {
	nodeReadFailure(c, err)
}

func (h *EgressHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status", "protocol", "country_code", "search")
	if err != nil {
		egressFailure(c, err)
		return
	}
	page, size, err := approvalPage(q)
	if err != nil {
		egressFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.List(c.Request.Context(), p, EgressFilter{Page: page, PageSize: size, Status: q.Get("status"),
		Protocol: q.Get("protocol"), CountryCode: q.Get("country_code"), Search: q.Get("search")})
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *EgressHandler) preview(c *gin.Context) {
	var in EgressImportInput
	if _, err := egressBody(c, &in, importBodyMax); err != nil {
		egressFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Preview(c.Request.Context(), p, in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *EgressHandler) importNow(c *gin.Context) {
	var in EgressImportInput
	raw, err := egressBody(c, &in, importBodyMax)
	if err != nil {
		egressFailure(c, err)
		return
	}
	out, err := h.Service.Import(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (h *EgressHandler) update(c *gin.Context) {
	var in EgressUpdate
	raw, err := egressBody(c, &in, 8<<10)
	if err != nil {
		egressFailure(c, err)
		return
	}
	out, err := h.Service.Update(c.Request.Context(), approvalProof(c, raw), c.Param("id"), in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

func (h *EgressHandler) subscriptions(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		egressFailure(c, badEgress("不接受查询参数"))
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Subscriptions(c.Request.Context(), p)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *EgressHandler) createSubscription(c *gin.Context) {
	var in SubscriptionInput
	raw, err := egressBody(c, &in, 8<<10)
	if err != nil {
		egressFailure(c, err)
		return
	}
	out, err := h.Service.CreateSubscription(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (h *EgressHandler) previewSubscription(c *gin.Context) {
	var in SubscriptionInput
	if _, err := egressBody(c, &in, 8<<10); err != nil {
		egressFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.SubscriptionPreview(c.Request.Context(), p, in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *EgressHandler) refreshSubscription(c *gin.Context) {
	var in SubscriptionRefresh
	raw, err := egressBody(c, &in, 8<<10)
	if err != nil {
		egressFailure(c, err)
		return
	}
	out, err := h.Service.RefreshSubscription(c.Request.Context(), approvalProof(c, raw), c.Param("id"), in)
	if err != nil {
		egressFailure(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}
