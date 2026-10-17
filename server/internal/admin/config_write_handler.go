package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type ConfigHandler struct {
	Auth  *Handler
	Write *ConfigWriteService
}

func (h *ConfigHandler) Register(g *gin.RouterGroup) {
	secured := g.Group("", h.Auth.Middleware())
	secured.PATCH("/configs/:key", h.update)
}

func (h *ConfigHandler) update(c *gin.Context) {
	var in ConfigUpdateInput
	raw, err := nodeWriteBody(c, &in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	out, err := h.Write.Update(c.Request.Context(), approvalProof(c, raw), c.Param("key"), in)
	if err != nil {
		nodeReadFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}
