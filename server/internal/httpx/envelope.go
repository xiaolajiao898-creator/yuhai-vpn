package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/safelog"
)

const HeaderRequestID = "X-Request-Id"

type successBody struct {
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type errorBody struct {
	Error     *Error `json:"error"`
	RequestID string `json:"request_id"`
}

func OK(c *gin.Context, data any)      { c.JSON(http.StatusOK, successBody{data, RequestID(c)}) }
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, successBody{data, RequestID(c)}) }

// Fail 写入错误响应；非 *Error 的错误一律折叠为 50001，不泄漏内部信息。
func Fail(c *gin.Context, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.ErrorContext(c.Request.Context(), "internal error", "request_id", RequestID(c), "err", safelog.Error(err))
		e = Internal
	} else if e.Code.HTTPStatus() >= 500 {
		slog.ErrorContext(c.Request.Context(), "server error", "request_id", RequestID(c), "err", safelog.Error(err))
		// Preserve documented retry/error semantics, but never expose arbitrary
		// server error messages or details that may contain a wrapped secret.
		messages := map[Code]string{
			ErrInternal: "服务器内部错误", ErrNoAmountSlot: "暂时没有可用收款通道",
			ErrPayQueueFull:    "99 个付款尾数都在使用中，请稍后再试",
			ErrNodeUnavailable: "节点暂不可用", ErrMaintenance: "服务维护中",
			ErrWGAddressExhausted: "隧道地址暂不可用",
		}
		if message, ok := messages[e.Code]; ok {
			e = NewError(e.Code, message)
		} else {
			e = Internal
		}
	}
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(e.Code.HTTPStatus(), errorBody{e, RequestID(c)})
}

func RequestID(c *gin.Context) string { return c.GetString(ctxRequestID) }
