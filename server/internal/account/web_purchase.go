package account

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/skip2/go-qrcode"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
	"github.com/yuhai-vpn/server/internal/order"
)

type webQuoteBody struct {
	PlanCode string `json:"plan_code"`
}

type webOrderBody struct {
	QuoteID        string `json:"quote_id"`
	PayMethod      string `json:"pay_method"`
	IdempotencyKey string `json:"idempotency_key"`
}

type webOrder struct {
	order.View
	PayDisplay string   `json:"pay_display"`
	QRModules  []string `json:"qr_modules,omitempty"`
}

func (h *Handler) mountWebPurchase(web *gin.RouterGroup, ip func(*gin.Context) string) {
	web.GET("/plans", h.requireWebSession,
		h.MW.RateLimitMessage("web_plans_user", 60, time.Minute, false, "查询太频繁", webUserKey),
		h.webPlans)
	web.POST("/quotes", h.requireWebSession,
		h.MW.RateLimitMessage("web_quote_user", 60, time.Hour, true, "报价太频繁", webUserKey),
		h.MW.RateLimitMessage("web_quote_ip", 120, time.Hour, true, "报价太频繁", ip),
		h.webQuote)
	web.POST("/orders", h.requireWebSession,
		h.MW.RateLimitMessage("web_order_user", 10, time.Hour, true, "下单太频繁", webUserKey),
		h.MW.RateLimitMessage("web_order_ip", 30, time.Hour, true, "下单太频繁", ip),
		h.webCreateOrder)
	web.GET("/orders/:order_no", h.requireWebSession,
		h.MW.RateLimitMessage("web_order_poll_user", 60, time.Minute, false, "查询太频繁", webUserKey),
		h.webGetOrder)
	web.POST("/orders/:order_no/cancel", h.requireWebSession,
		h.MW.RateLimitMessage("web_order_cancel_user", 30, time.Hour, true, "操作太频繁", webUserKey),
		h.webCancelOrder)
}

func (h *Handler) webPlans(c *gin.Context) {
	if h.Plans == nil {
		httpx.Fail(c, httpx.Internal)
		return
	}
	items, err := h.Plans.List(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, gin.H{"items": items})
}

func (h *Handler) webQuote(c *gin.Context) {
	if h.Plans == nil {
		httpx.Fail(c, httpx.Internal)
		return
	}
	var body webQuoteBody
	if _, ok := readJSON(c, &body); !ok {
		return
	}
	code := strings.TrimSpace(body.PlanCode)
	if code == "" || len(code) > 32 {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请选择套餐"))
		return
	}
	q, err := h.Plans.Quote(c.Request.Context(), webUserID(c), code)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, q)
}

func (h *Handler) webCreateOrder(c *gin.Context) {
	if h.Orders == nil {
		httpx.Fail(c, httpx.Internal)
		return
	}
	var body webOrderBody
	if _, ok := readJSON(c, &body); !ok {
		return
	}
	if _, err := uuid.Parse(body.QuoteID); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "报价无效"))
		return
	}
	if _, err := uuid.Parse(body.IdempotencyKey); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请重新发起购买"))
		return
	}
	if body.PayMethod != "USDT_TRC20" {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "网页购买只支持 USDT（TRC20）"))
		return
	}
	v, err := h.Orders.Create(c.Request.Context(), webUserID(c), order.CreateInput{
		OrderType: "PLAN", QuoteID: body.QuoteID, PayMethod: body.PayMethod,
	}, body.IdempotencyKey)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.Created(c, presentWebOrder(v))
}

func (h *Handler) webGetOrder(c *gin.Context) {
	if h.Orders == nil {
		httpx.Fail(c, httpx.Internal)
		return
	}
	v, err := h.Orders.Get(c.Request.Context(), webUserID(c), c.Param("order_no"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, presentWebOrder(v))
}

func (h *Handler) webCancelOrder(c *gin.Context) {
	if h.Orders == nil {
		httpx.Fail(c, httpx.Internal)
		return
	}
	v, err := h.Orders.Cancel(c.Request.Context(), webUserID(c), c.Param("order_no"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, presentWebOrder(v))
}

func presentWebOrder(v order.View) webOrder {
	out := webOrder{View: v, PayDisplay: v.PayAmount}
	if m, err := money.Parse(v.PayAmount); err == nil {
		out.PayDisplay = m.Display()
	}
	if v.ReceiveAddress != "" && (v.Status == "PENDING" || v.Status == "CONFIRMING" || v.Status == "EXPIRED") {
		if rows, err := qrModules(v.ReceiveAddress); err == nil {
			out.QRModules = rows
		}
	}
	return out
}

func qrModules(text string) ([]string, error) {
	code, err := qrcode.New(text, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	bm := code.Bitmap()
	rows := make([]string, len(bm))
	for i, row := range bm {
		var b strings.Builder
		b.Grow(len(row))
		for _, on := range row {
			if on {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}
		}
		rows[i] = b.String()
	}
	return rows, nil
}
