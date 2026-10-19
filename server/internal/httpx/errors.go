// Package httpx 定义统一响应结构、错误码与通用中间件（方案 11.1、11.7）。
package httpx

import (
	"fmt"
	"net/http"
)

// Code 为 5 位错误码：前 3 位 = HTTP 状态码，后 2 位 = 序号。
type Code int

func (c Code) HTTPStatus() int { return int(c) / 100 }

// 错误码表（与方案 11.7 一一对应；新增请同步更新方案与 App 端）
const (
	ErrBadRequest        Code = 40001
	ErrAmountFormat      Code = 40002
	ErrTokenMissing      Code = 40101
	ErrSignatureInvalid  Code = 40102
	ErrRefreshReplay     Code = 40103
	ErrSecretInvalid     Code = 40104
	ErrTimestampSkew     Code = 40105
	ErrNonceReplay       Code = 40106
	ErrDeviceRevoked     Code = 40107
	ErrSecretRotated     Code = 40108
	ErrStepUpRequired    Code = 40110 // 后台
	ErrStepUpInvalid     Code = 40111
	ErrTOTPInvalid       Code = 40112
	ErrLoginInvalid      Code = 40113 // 地址或密码错误
	ErrInsufficientFunds Code = 40201
	ErrNoPlan            Code = 40301
	ErrTierTooLow        Code = 40302
	ErrQuotaExhausted    Code = 40303
	ErrRiskBlocked       Code = 40304
	ErrAccountDisabled   Code = 40305
	ErrTrialDisabled     Code = 40311 // 免费试用已关闭
	ErrForbidden         Code = 40310 // 后台/节点
	ErrNotFound          Code = 40401
	ErrInviteNotFound    Code = 40402
	ErrConflict          Code = 40901
	ErrDeviceLimit       Code = 40902
	ErrInviteBound       Code = 40903
	ErrInviteWindow      Code = 40904
	ErrOrderState        Code = 40905
	ErrTooManyPending    Code = 40906
	ErrApproverIsAuthor  Code = 40907
	ErrApprovalStale     Code = 40908
	ErrAddressTaken      Code = 40909 // 地址已注册
	ErrResetNotPaid      Code = 40910 // 找回密码：还没查到验证转账
	ErrQuoteExpired      Code = 41001
	ErrSelfInvite        Code = 42201
	ErrPlanChanged       Code = 42202
	ErrTopupRange        Code = 42203
	ErrVersionTooLow     Code = 42601
	ErrRateLimited       Code = 42901
	ErrRestoreRateLimit  Code = 42902
	ErrInternal          Code = 50001
	ErrNoAmountSlot      Code = 50301
	ErrNodeUnavailable   Code = 50302
	ErrMaintenance       Code = 50303
	// 隧道地址池已耗尽：运维需扩容地址池，不是客户端可以重试解决的问题。
	// 方案 11.7 的错误码表需同步补这一行。
	ErrWGAddressExhausted Code = 50304
)

// Error 是可直接写回响应的业务错误。Message 为默认中文文案，网关按 Accept-Language 替换。
type Error struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%d %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%d %s", e.Code, e.Message)
}
func (e *Error) Unwrap() error { return e.cause }

func NewError(code Code, msg string) *Error { return &Error{Code: code, Message: msg} }
func (e *Error) WithDetails(d map[string]any) *Error {
	c := *e
	c.Details = d
	return &c
}
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.cause = err
	return &c
}

var Internal = NewError(ErrInternal, "服务器内部错误")

// Sanity check：错误码前三位必须是合法 HTTP 状态码
func init() {
	for _, c := range []Code{ErrBadRequest, ErrTokenMissing, ErrInsufficientFunds, ErrNoPlan, ErrNotFound,
		ErrConflict, ErrQuoteExpired, ErrSelfInvite, ErrVersionTooLow, ErrRateLimited, ErrInternal, ErrNoAmountSlot,
		ErrWGAddressExhausted} {
		if http.StatusText(c.HTTPStatus()) == "" {
			panic(fmt.Sprintf("非法错误码 %d", c))
		}
	}
}
