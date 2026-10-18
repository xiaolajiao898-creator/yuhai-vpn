package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/httpx"
)

type RedeemHandler struct {
	Auth  *Handler
	Read  *RedeemReadService
	Write *RedeemWriteService
}

func (h *RedeemHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/redeem-card-types", h.cardTypes)
	secured.GET("/redeem-codes", h.list)
	secured.POST("/redeem-codes/batches", h.generate)
	secured.POST("/redeem-codes/batches/:id/export", h.export)
	secured.POST("/redeem-codes/:id/revoke", h.revoke)
}

func (h *RedeemHandler) cardTypes(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "查询参数无效"))
		return
	}
	p, _ := Current(c)
	out, err := h.Read.CardTypes(c.Request.Context(), p)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *RedeemHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "card_type", "status", "batch_id", "note")
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
	out, err := h.Read.List(c.Request.Context(), p, CodeFilter{
		Page: page, PageSize: size, CardType: q.Get("card_type"), Status: q.Get("status"),
		BatchID: q.Get("batch_id"), Note: q.Get("note"),
	})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *RedeemHandler) generate(c *gin.Context) {
	var in GenerateInput
	raw, err := redeemBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.Generate(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *RedeemHandler) export(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in ReasonInput
	raw, err := redeemBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.Export(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func (h *RedeemHandler) revoke(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in ReasonInput
	raw, err := redeemBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.Revoke(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

func redeemBody(c *gin.Context, out any) ([]byte, error) {
	bad := httpx.NewError(httpx.ErrBadRequest, "请求体必须是 JSON 对象，且不含未知字段")
	mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mt != "application/json" || c.Request.URL.RawQuery != "" {
		return nil, bad
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10))
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
