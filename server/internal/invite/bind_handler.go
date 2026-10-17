package invite

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
)

func (h *Handler) bind(c *gin.Context) {
	if h.Binder == nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "邀请绑定暂未开放"))
		return
	}
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体无效或过大"))
		return
	}
	var input BindInput
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	result, err := h.Binder.Bind(c.Request.Context(), httpx.UserID(c), input, c.GetHeader("Idempotency-Key"))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, result)
}
