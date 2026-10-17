package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type InviteHandler struct {
	Auth   *Handler
	Read   *InviteReadService
	Review *InviteReviewService
	Rules  *InviteRuleWriteService
}

func (h *InviteHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.GET("/invite-relations", h.relations)
	secured.GET("/invite-rewards", h.rewards)
	secured.POST("/invite-rewards/:id/review", h.review)
	secured.PATCH("/invite-rules/:id", h.updateRule)
}

func (h *InviteHandler) relations(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "inviter_user_id", "invitee_user_id")
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
	out, err := h.Read.Relations(c.Request.Context(), p, InviteFilter{Page: page, PageSize: size,
		InviterUserID: q.Get("inviter_user_id"), InviteeUserID: q.Get("invitee_user_id")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *InviteHandler) rewards(c *gin.Context) {
	q, err := approvalQuery(c, "page", "page_size", "status", "beneficiary_user_id")
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
	out, err := h.Read.Rewards(c.Request.Context(), p, InviteFilter{Page: page, PageSize: size,
		Status: q.Get("status"), BeneficiaryID: q.Get("beneficiary_user_id")})
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *InviteHandler) review(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in RewardReviewInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Review.Review(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// updateRule 修改一条奖励规则。不动已经生成的奖励——它们按各自的 rule_snapshot 结算。
func (h *InviteHandler) updateRule(c *gin.Context) {
	id, err := positiveApprovalID(c.Param("id"))
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	var in InviteRuleUpdateInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Rules.Update(c.Request.Context(), approvalProof(c, raw), id, in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}
