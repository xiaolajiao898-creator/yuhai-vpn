package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redisx"
)

// DevicePrincipal 是校验 S1/S2 请求所需的最小数据。
type DevicePrincipal struct {
	DeviceID      int64
	UserID        int64
	DeviceUID     string
	PubKeySPKI    string
	DeviceActive  bool
	SecretVersion int
	UserStatus    string
}

type PrincipalStore interface {
	LookupDevice(ctx context.Context, deviceID int64) (DevicePrincipal, error)
}

type Middleware struct {
	Signer *Signer
	Store  PrincipalStore
	Redis  *redisx.Client
	Clock  clock.Clock
}

const maxBody = 64 << 10

// S1：访问令牌。
func (m *Middleware) S1() gin.HandlerFunc { return m.handle(false, false) }

// S2：访问令牌 + 设备签名 + nonce。
func (m *Middleware) S2() gin.HandlerFunc { return m.handle(true, false) }

// S2AllowExpired：刷新令牌接口，访问令牌可过期但签名必须有效。
func (m *Middleware) S2AllowExpired() gin.HandlerFunc { return m.handle(true, true) }

func (m *Middleware) handle(requireSig, allowExpired bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := m.Clock.Now()
		claims, err := m.bearer(c, now, allowExpired)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		p, err := m.Store.LookupDevice(c.Request.Context(), claims.DeviceID)
		if err != nil {
			httpx.Fail(c, httpx.NewError(httpx.ErrDeviceRevoked, "此设备已被移除"))
			return
		}
		if p.DeviceID != claims.DeviceID || p.UserID != claims.Subject || p.UserID <= 0 || !p.DeviceActive {
			httpx.Fail(c, httpx.NewError(httpx.ErrDeviceRevoked, "此设备已被移除"))
			return
		}
		if p.SecretVersion != claims.SecretVersion {
			httpx.Fail(c, httpx.NewError(httpx.ErrSecretRotated, "账户密钥已重置，请用新密钥恢复"))
			return
		}
		if Thumbprint(p.PubKeySPKI) != claims.KeyThumbprint {
			httpx.Fail(c, httpx.NewError(httpx.ErrSignatureInvalid, "设备签名无效"))
			return
		}
		if p.UserStatus == "BANNED" {
			httpx.Fail(c, httpx.NewError(httpx.ErrAccountDisabled, "账户已停用"))
			return
		}
		if requireSig {
			if err := m.verifySignature(c, now, p); err != nil {
				httpx.Fail(c, err)
				return
			}
		}
		httpx.SetPrincipal(c, p.UserID, p.DeviceID)
		c.Next()
	}
}

func (m *Middleware) bearer(c *gin.Context, now time.Time, allowExpired bool) (AccessClaims, error) {
	h := c.GetHeader("Authorization")
	if len(h) < 8 || h[:7] != "Bearer " {
		return AccessClaims{}, httpx.NewError(httpx.ErrTokenMissing, "访问令牌缺失")
	}
	claims, err := m.Signer.Verify(h[7:], now, allowExpired)
	if err != nil {
		if errors.Is(err, ErrTokenExpired) {
			return claims, httpx.NewError(httpx.ErrTokenMissing, "访问令牌已过期")
		}
		return claims, httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
	}
	if claims.DeviceID <= 0 || claims.Subject <= 0 || claims.SecretVersion <= 0 || claims.KeyThumbprint == "" {
		return claims, httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
	}
	return claims, nil
}

// verifySignature 按方案 12.2 顺序：时间戳 → 公钥验签 → nonce。
func (m *Middleware) verifySignature(c *gin.Context, now time.Time, p DevicePrincipal) error {
	deviceUID, ts, nonce, sig := Headers(c.Request)
	if deviceUID != p.DeviceUID || !validNonce(nonce) {
		return httpx.NewError(httpx.ErrSignatureInvalid, "设备签名无效")
	}
	if err := CheckTimestamp(ts, now); err != nil {
		return httpx.NewError(httpx.ErrTimestampSkew, "时间戳偏差过大").WithDetails(map[string]any{"server_time": now.Format(time.RFC3339)})
	}
	body, err := ReadBody(c)
	if err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "请求体过大")
	}
	pub, err := ParsePublicKey(p.PubKeySPKI)
	if err != nil {
		return httpx.Internal.Wrap(err)
	}
	canonical := CanonicalRequest(c.Request.Method, c.Request.URL.Path, c.Request.URL.Query(), ts, nonce, deviceUID, body)
	if !Verify(pub, canonical, sig) {
		return httpx.NewError(httpx.ErrSignatureInvalid, "设备签名无效")
	}
	ok, err := m.Redis.ClaimNonce(c.Request.Context(), p.DeviceID, nonce, NonceTTL)
	if err != nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err) // Redis 不可用：写操作 fail-closed
	}
	if !ok {
		return httpx.NewError(httpx.ErrNonceReplay, "nonce 重复")
	}
	return nil
}

// ReadBody 读取并回放请求体（限制 64 KiB），供签名校验与后续绑定共用。
func ReadBody(c *gin.Context) ([]byte, error) {
	if b, ok := c.Get("yh.body"); ok {
		return b.([]byte), nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errors.New("body too large")
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Set("yh.body", body)
	return body, nil
}

// VerifySelfSigned 用于 S2-self（注册/恢复）：请求体携带公钥，用该公钥验证签名，证明持有私钥。
func (m *Middleware) VerifySelfSigned(c *gin.Context, pubkeySPKI string, body []byte) error {
	deviceUID, ts, nonce, sig := Headers(c.Request)
	if deviceUID == "" || len(deviceUID) > 128 || !validNonce(nonce) {
		return httpx.NewError(httpx.ErrSignatureInvalid, "设备签名无效")
	}
	now := m.Clock.Now()
	if err := CheckTimestamp(ts, now); err != nil {
		return httpx.NewError(httpx.ErrTimestampSkew, "时间戳偏差过大").WithDetails(map[string]any{"server_time": now.Format(time.RFC3339)})
	}
	pub, err := ParsePublicKey(pubkeySPKI)
	if err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "设备公钥格式错误")
	}
	canonical := CanonicalRequest(c.Request.Method, c.Request.URL.Path, c.Request.URL.Query(), ts, nonce, deviceUID, body)
	if !Verify(pub, canonical, sig) {
		return httpx.NewError(httpx.ErrSignatureInvalid, "设备签名无效")
	}
	// 自签阶段还没有 device_id，用公钥指纹作 nonce 命名空间
	ok, err := m.Redis.ClaimNonce(c.Request.Context(), 0, Thumbprint(pubkeySPKI)+":"+nonce, NonceTTL)
	if err != nil {
		return httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
	}
	if !ok {
		return httpx.NewError(httpx.ErrNonceReplay, "nonce 重复")
	}
	return nil
}

// RateLimit 限流中间件；keyFn 返回空串表示不限流。failClosed 决定 Redis 不可用时的行为。
func (m *Middleware) RateLimit(name string, limit int, window time.Duration, failClosed bool, keyFn func(*gin.Context) string) gin.HandlerFunc {
	return m.RateLimitMessage(name, limit, window, failClosed, "请求过于频繁", keyFn)
}

// RateLimitMessage 与 RateLimit 相同，但命中限额时使用调用方指定的中文提示。
func (m *Middleware) RateLimitMessage(name string, limit int, window time.Duration, failClosed bool, message string, keyFn func(*gin.Context) string) gin.HandlerFunc {
	if message == "" {
		message = "请求过于频繁"
	}
	return func(c *gin.Context) {
		k := keyFn(c)
		if k == "" {
			c.Next()
			return
		}
		ok, retry, err := m.Redis.Allow(c.Request.Context(), name+":"+k, limit, window)
		if err != nil {
			if failClosed {
				httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err))
				return
			}
			c.Next()
			return
		}
		if !ok {
			c.Header("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			httpx.Fail(c, httpx.NewError(httpx.ErrRateLimited, message))
			return
		}
		c.Next()
	}
}

// ClientIP 供限流和审计使用，仅按 Gin 显式配置的可信代理解析客户端地址。
// 不读取客户端可自行填写的 X-Client-Ip-Hash，否则每次换值即可绕过 IP 限流。
func ClientIP(c *gin.Context) string {
	return c.ClientIP()
}
