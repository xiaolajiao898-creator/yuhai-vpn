package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type FinanceReadHandler struct {
	Auth    *Handler
	Service *FinanceReadService
	// Exceptions 是同一组路由下唯一的写操作：到账异常处置（L3 双人审批）。
	// 它与只读服务挂在一起，是因为契约把它放在 /deposit-exceptions 之下，
	// 拆成两个 handler 只会让「同一段路径由两处注册」更难查。
	Exceptions *DepositExceptionService
}

func (h *FinanceReadHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/orders", h.orders)
	secured.GET("/orders/:order_no", h.order)
	secured.GET("/chain-transfers", h.transfers)
	secured.GET("/deposit-exceptions", h.exceptions)
	secured.POST("/deposit-exceptions/:id/resolve", h.resolveException)
	secured.GET("/receive-addresses", h.addresses)
	secured.GET("/scan-cursors", h.cursors)
}

func (h *FinanceReadHandler) orders(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status", "user_id", "order_no")
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
	out, err := h.Service.Orders(c.Request.Context(), p, FinanceFilter{Page: page, PageSize: size,
		Status: q.Get("status"), UserID: q.Get("user_id"), OrderNo: q.Get("order_no")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceReadHandler) order(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.Order(c.Request.Context(), p, c.Param("order_no"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceReadHandler) transfers(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "match_status", "tx_hash")
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
	out, err := h.Service.ChainTransfers(c.Request.Context(), p, FinanceFilter{Page: page, PageSize: size,
		MatchStatus: q.Get("match_status"), TxHash: q.Get("tx_hash")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceReadHandler) exceptions(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status")
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
	out, err := h.Service.DepositExceptions(c.Request.Context(), p,
		FinanceFilter{Page: page, PageSize: size, Status: q.Get("status")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceReadHandler) addresses(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.ReceiveAddresses(c.Request.Context(), p)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *FinanceReadHandler) cursors(c *gin.Context) {
	if _, err := approvalQuery(c); err != nil {
		nodeReadFailure(c, err)
		return
	}
	p, _ := Current(c)
	out, err := h.Service.ScanCursors(c.Request.Context(), p)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ---------- 风控（黑名单只读 + L2 写） ----------

type RiskHandler struct {
	Auth  *Handler
	Read  *RiskReadService
	Write *RiskWriteService
}

func (h *RiskHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/blacklist", h.list)
	secured.POST("/blacklist", h.add)
	secured.DELETE("/blacklist/:id", h.remove)
	secured.POST("/users/:id/status", h.userStatus)
}

func (h *RiskHandler) list(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status", "target_type")
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
	out, err := h.Read.Blacklist(c.Request.Context(), p,
		FinanceFilter{Page: page, PageSize: size, Status: q.Get("status")}, q.Get("target_type"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *RiskHandler) add(c *gin.Context) {
	var in BlacklistAddInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.AddBlacklist(c.Request.Context(), approvalProof(c, raw), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, out)
}

func (h *RiskHandler) remove(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in RiskReasonInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	if err := h.Write.RemoveBlacklist(c.Request.Context(), approvalProof(c, raw), id, in); err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

func (h *RiskHandler) userStatus(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in UserStatusInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	if err := h.Write.SetUserStatus(c.Request.Context(), approvalProof(c, raw), id, in); err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

// resolveException 发起到账异常处置审批。返回 202 与审批单编号——
// **这时钱还没有动**，要等另一名管理员在审批中心点通过。
func (h *FinanceReadHandler) resolveException(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in ExceptionResolveInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Exceptions.RequestResolve(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, gin.H{"approval_id": out.ID})
}
