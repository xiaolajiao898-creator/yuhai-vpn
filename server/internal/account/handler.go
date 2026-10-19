package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/redeem"
	"github.com/yuhai-vpn/server/internal/trial"
)

type Handler struct {
	Svc     *Service
	MW      *auth.Middleware
	Trials  *trial.Service
	Redeems *redeem.Service
}

// Mount 挂载账户相关路由（方案 11.3）。限流阈值见 11.8。
func (h *Handler) Mount(v1 *gin.RouterGroup) {
	ip := func(c *gin.Context) string { return auth.ClientIP(c) }
	prefix := func(c *gin.Context) string { return ipPrefix(auth.ClientIP(c)) }
	dev := func(c *gin.Context) string { return c.GetHeader(auth.HeaderDeviceID) }
	user := func(c *gin.Context) string { return itoa64(httpx.UserID(c)) }

	a := v1.Group("/auth")
	a.POST("/register",
		h.MW.RateLimit("register_ip", 5, time.Hour, true, ip),
		h.MW.RateLimit("register_prefix", 30, time.Hour, true, prefix),
		h.register)
	a.POST("/restore",
		h.MW.RateLimit("restore_ip", 10, time.Hour, true, ip),
		h.restore)
	a.POST("/restore/replace-device", h.MW.RateLimit("restore_ip", 10, time.Hour, true, ip), h.replaceDevice)
	a.POST("/refresh", h.MW.RateLimit("refresh_dev", 30, time.Hour, false, dev), h.MW.S2AllowExpired(), h.refresh)
	a.POST("/logout", h.MW.S2(), h.logout)

	h.mountPassword(v1)
	h.mountWeb(v1)
	h.mountLedgers(v1)
	v1.GET("/me", h.MW.S1(), h.me)
	v1.POST("/account/backup-ack", h.MW.S2(), h.backupAck)
	// 重置密钥会把其余设备一次性全部下线，而且不可逆。限流按账户而不是按设备：
	// 按设备的话换一台接着刷就绕过去了。Redis 不可用时 fail-closed。
	v1.POST("/account/secret/reset", h.MW.S2(),
		h.MW.RateLimit("secret_reset_user", 3, 24*time.Hour, true, user), h.resetSecret)
	v1.GET("/devices", h.MW.S1(), h.MW.RateLimit("devices_user", 60, time.Minute, false, user), h.devices)
	v1.DELETE("/devices/:device_id", h.MW.S2(), h.MW.RateLimit("device_remove_user", 30, time.Minute, true, user), h.removeDevice)
	// 上传本设备 WireGuard 公钥。限流按设备：正常客户端只在首次和轮换时调用。
	v1.PUT("/devices/current/wg-key", h.MW.S2(), h.MW.RateLimit("wg_key_dev", 10, time.Hour, true, dev), h.putWGKey)
}

func (h *Handler) register(c *gin.Context) {
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	var in RegisterInput
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	idem := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(idem); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	if c.GetHeader(auth.HeaderDeviceID) != in.DeviceUID {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "X-Device-Id 与 device_uid 不一致"))
		return
	}
	ipHash, prefixHash := hashIP(auth.ClientIP(c))
	res, status, err := h.Svc.Register(c.Request.Context(), in, idem, body, ipHash, prefixHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"data": res, "request_id": httpx.RequestID(c)})
}

func (h *Handler) restore(c *gin.Context) {
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	var in RestoreInput
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 同一密钥查找键 20 次/天（不能在限流层做，因为需要先规范化）
	if n, _ := h.lookupKeyOf(in.AccountSecret); n != "" {
		if ok, retry, err := h.MW.Redis.Allow(c.Request.Context(), "restore_key:"+n, 20, 24*time.Hour); err != nil {
			httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试"))
			return
		} else if !ok {
			c.Header("Retry-After", itoaDur(retry))
			httpx.Fail(c, httpx.NewError(httpx.ErrRestoreRateLimit, "恢复尝试次数过多"))
			return
		}
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	res, err := h.Svc.Restore(c.Request.Context(), in, ipHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

func (h *Handler) replaceDevice(c *gin.Context) {
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	var in ReplaceDeviceInput
	if err := json.Unmarshal(body, &in); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	res, err := h.Svc.ReplaceDevice(c.Request.Context(), in, ipHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

func (h *Handler) refresh(c *gin.Context) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || !strings.HasPrefix(in.RefreshToken, "rt_") {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return
	}
	tokens, err := h.Svc.Refresh(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), in.RefreshToken)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, tokens)
}

func (h *Handler) logout(c *gin.Context) {
	ipHash, _ := hashIP(auth.ClientIP(c))
	if err := h.Svc.Logout(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), ipHash); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) me(c *gin.Context) {
	me, err := h.Svc.Me(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, me)
}

func (h *Handler) backupAck(c *gin.Context) {
	if err := h.Svc.AckBackup(c.Request.Context(), httpx.UserID(c)); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) devices(c *gin.Context) {
	devices, err := h.Svc.Repo.listDevices(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	cur := httpx.DeviceID(c)
	for i := range devices {
		devices[i].IsCurrent = devices[i].ID == itoa64(cur)
	}
	limit, err := h.Svc.Repo.deviceLimit(c.Request.Context(), h.Svc.Repo.Pool, httpx.UserID(c), h.Svc.Clock.Now())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"devices": devices, "device_limit": limit})
}

// ---- helpers ----

func (h *Handler) lookupKeyOf(secret string) (string, error) {
	n, err := ids.NormalizeSecret(secret)
	if err != nil {
		return "", err
	}
	return hashStr(n)[:16], nil
}

// hashIP 返回 IP 哈希与 /24（IPv6 /48）前缀哈希；只存哈希，30 天后置空。
func hashIP(ip string) (string, string) {
	if ip == "" {
		return "", ""
	}
	return hashStr(ip), hashStr(ipPrefix(ip))
}

func ipPrefix(ip string) string {
	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		if len(parts) >= 3 {
			return strings.Join(parts[:3], ":") + "::/48"
		}
		return ip
	}
	if i := strings.LastIndexByte(ip, '.'); i > 0 {
		return ip[:i] + ".0/24"
	}
	return ip
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

func itoaDur(d time.Duration) string { return strconv.FormatInt(int64(d.Seconds())+1, 10) }

func hashStr(s string) string {
	x := sha256.Sum256([]byte(s))
	return hex.EncodeToString(x[:])
}
