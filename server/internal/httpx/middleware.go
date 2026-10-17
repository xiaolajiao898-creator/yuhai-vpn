package httpx

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders applies headers that are safe for the JSON APIs and the
// admin surface. The API never serves HTML, so a restrictive CSP is useful as
// a defense-in-depth control without affecting normal clients.
func SecurityHeaders(production bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if production {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

const (
	ctxRequestID = "yh.request_id"
	ctxUserID    = "yh.user_id"
	ctxDeviceID  = "yh.device_id"
)

// RequestIDMiddleware 生成 request_id 并同时放入响应头与响应体。
func RequestIDMiddleware() gin.HandlerFunc {
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return func(c *gin.Context) {
		b := make([]byte, 10)
		_, _ = rand.Read(b)
		id := enc.EncodeToString(b)
		c.Set(ctxRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// RecoveryMiddleware 把 panic 折叠为 50001。
func RecoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic", "request_id", RequestID(c), "panic_type", fmt.Sprintf("%T", r))
				Fail(c, Internal)
			}
		}()
		c.Next()
	}
}

// AccessLogMiddleware 输出脱敏访问日志：不记录请求体、不记录 Authorization、IP 只在网关层做哈希。
func AccessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		slog.Info("http",
			"request_id", RequestID(c),
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(),
			"device", c.GetString(ctxDeviceID),
		)
	}
}

// 鉴权级别（方案 11.2）。实现位于 internal/auth，这里只定义在路由上声明级别的方式。
type AuthLevel string

const (
	S0         AuthLevel = "S0"      // 无需登录
	S1         AuthLevel = "S1"      // 访问令牌
	S2         AuthLevel = "S2"      // 访问令牌 + 设备签名 + nonce
	S2SelfSign AuthLevel = "S2-self" // 请求体携带公钥并自签（注册/恢复）
)

func UserID(c *gin.Context) int64   { return c.GetInt64(ctxUserID) }
func DeviceID(c *gin.Context) int64 { return c.GetInt64(ctxDeviceID) }
func SetPrincipal(c *gin.Context, userID, deviceID int64) {
	c.Set(ctxUserID, userID)
	c.Set(ctxDeviceID, deviceID)
}
