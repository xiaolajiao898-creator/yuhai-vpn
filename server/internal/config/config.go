// Package config 从环境变量加载配置，并执行“生产环境不得带开发开关”的启动校验（方案 11.6、12.10）。
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/yuhai-vpn/server/internal/tron"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

type Config struct {
	Env Env

	DatabaseURL string
	RedisURL    string

	APIListen     string
	NodeAPIListen string
	AdminListen   string

	SecretPepper   []byte // HMAC pepper：账户密钥查找键
	JWTSeed        []byte // Ed25519 种子
	JWTKeyID       string
	FieldEncKey    []byte // 字段信封加密数据密钥（开发环境直接给；生产由 KMS 解封）
	FieldEncKeyID  string
	FundAddrPubKey string // 可选。只用于校验旧的资金密钥签名；新收款地址由管理员审批，不需要这把公钥
	FundAddrKeyID  string
	TronGridAPIKey string
	// 忘记密码时用户转 TRX 的官方收款地址（只收不付，不需要私钥）。为空则找回功能关闭。
	PasswordResetPayTo string
	USDTContract       string
	DevToolsEnabled    bool
	// This runtime gate is independent of stored reward rules. False leaves
	// invitation events queued and prevents release of existing frozen rewards.
	InviteRewardsEnabled bool
	// 撤销执行回执对账。false 时 worker 不读不写回执表；撤销仍然照常排队，
	// 只是没有「已在节点生效」的确认记录。打开不会改变任何节点上的实际状态。
	RemovalReceiptsEnabled bool

	// WireGuard 隧道内地址池：设备首次上传公钥时从这里分配一个 /32。
	// 必须是私有网段——把公网地址分配进隧道会让用户流量走错方向，所以在启动时就拒绝。
	// 前 WGAddressReserved 个地址留给节点自身（网络地址、网关、隧道内 DNS 等），不分配给设备。
	WGAddressPool     *net.IPNet
	WGAddressReserved int
}

func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// Load 读取环境变量。缺少必需项或生产环境含开发开关时返回错误，调用方应直接退出。
func Load() (Config, error) {
	c := Config{
		Env:                Env(get("APP_ENV", string(EnvDevelopment))),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		RedisURL:           get("REDIS_URL", "redis://localhost:6379/0"),
		APIListen:          get("API_LISTEN", ":8080"),
		NodeAPIListen:      get("NODE_API_LISTEN", ":8443"),
		AdminListen:        get("ADMIN_LISTEN", ":8081"),
		JWTKeyID:           get("JWT_KEY_ID", "jwt-dev-1"),
		FieldEncKeyID:      get("FIELD_ENC_KEY_ID", "fek-dev-1"),
		FundAddrPubKey:     os.Getenv("FUND_ADDRESS_PUBKEY_B64"),
		FundAddrKeyID:      get("FUND_ADDRESS_KEY_ID", "fund-dev-1"),
		TronGridAPIKey:     os.Getenv("TRONGRID_API_KEY"),
		PasswordResetPayTo: strings.TrimSpace(os.Getenv("PASSWORD_RESET_PAY_TO")),
		USDTContract:       get("USDT_TRC20_CONTRACT", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"),
	}
	var err error
	if c.DevToolsEnabled, err = strictBool("DEV_TOOLS_ENABLED", false); err != nil {
		return c, err
	}
	if c.InviteRewardsEnabled, err = strictBool("INVITE_REWARDS_ENABLED", false); err != nil {
		return c, err
	}
	if c.RemovalReceiptsEnabled, err = strictBool("NODE_WG_RECEIPTS_ENABLED", false); err != nil {
		return c, err
	}
	if c.SecretPepper, err = hexKey("SECRET_PEPPER_HEX", 32); err != nil {
		return c, err
	}
	if c.JWTSeed, err = hexKey("JWT_ED25519_SEED_HEX", 32); err != nil {
		return c, err
	}
	if c.FieldEncKey, err = hexKey("FIELD_ENC_KEY_HEX", 32); err != nil {
		return c, err
	}
	if c.WGAddressPool, c.WGAddressReserved, err = wgPool(); err != nil {
		return c, err
	}
	return c, c.validate()
}

// wgPool 解析隧道地址池。故意把限制放在启动期而不是分配期：地址池配错的后果
// （把公网地址发给客户端当隧道地址）在运行时很难被发现，但在启动时一目了然。
func wgPool() (*net.IPNet, int, error) {
	raw := get("WG_ADDRESS_POOL", "10.20.0.0/16")
	_, pool, err := net.ParseCIDR(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("WG_ADDRESS_POOL 不是合法 CIDR: %q", raw)
	}
	if pool.IP.To4() == nil {
		return nil, 0, errors.New("WG_ADDRESS_POOL 目前只支持 IPv4")
	}
	if !pool.IP.IsPrivate() {
		return nil, 0, fmt.Errorf("WG_ADDRESS_POOL 必须是私有网段，收到 %q", raw)
	}
	ones, bits := pool.Mask.Size()
	// 上限 /12（约 104 万个地址）是为了让「池子还剩多少」始终是个可算的数；
	// 下限 /28 保证除去保留地址后至少还有几个能分配。
	if ones < 12 || ones > 28 {
		return nil, 0, fmt.Errorf("WG_ADDRESS_POOL 掩码必须在 /12 与 /28 之间，收到 /%d", ones)
	}
	reserved, err := strictInt("WG_ADDRESS_RESERVED", 16)
	if err != nil {
		return nil, 0, err
	}
	size := int64(1) << uint(bits-ones)
	// 末位是广播地址，同样不分配。
	if int64(reserved) < 1 || int64(reserved) >= size-1 {
		return nil, 0, fmt.Errorf("WG_ADDRESS_RESERVED 必须在 1 与 %d 之间，收到 %d", size-2, reserved)
	}
	return pool, reserved, nil
}

func strictInt(name string, defaultValue int) (int, error) {
	v, exists := os.LookupEnv(name)
	if !exists || v == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数", name)
	}
	return n, nil
}

func (c Config) validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL 未设置"))
	}
	switch c.Env {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV 非法: %q", c.Env))
	}
	if c.PasswordResetPayTo != "" && !tron.ValidAddress(c.PasswordResetPayTo) {
		errs = append(errs, errors.New("PASSWORD_RESET_PAY_TO 不是有效的 TRON 地址"))
	}
	if c.USDTContract != tron.OfficialUSDT {
		errs = append(errs, errors.New("USDT_TRC20_CONTRACT 只能是 TRON 官方 USDT（TRC20）合约"))
	}
	if c.IsProduction() {
		// 铁律：生产环境出现开发开关直接拒绝启动
		if c.DevToolsEnabled {
			errs = append(errs, errors.New("生产环境禁止 DEV_TOOLS_ENABLED=true"))
		}
		if c.InviteRewardsEnabled {
			errs = append(errs, errors.New("生产环境必须显式关闭 INVITE_REWARDS_ENABLED，待完整验收后再单独启用"))
		}
		if c.FundAddrPubKey != "" {
			if key, err := base64.StdEncoding.Strict().DecodeString(c.FundAddrPubKey); err != nil || len(key) != 32 || allSame(key) {
				errs = append(errs, errors.New("生产环境资金地址公钥格式无效"))
			}
		}
		if allSame(c.SecretPepper) || allSame(c.JWTSeed) || allSame(c.FieldEncKey) {
			errs = append(errs, errors.New("生产环境检测到示例密钥，拒绝启动"))
		}
		if bytes.Equal(c.SecretPepper, c.JWTSeed) || bytes.Equal(c.JWTSeed, c.FieldEncKey) || bytes.Equal(c.FieldEncKey, c.SecretPepper) {
			errs = append(errs, errors.New("生产环境认证与加密密钥必须独立"))
		}
		for name, id := range map[string]string{"JWT_KEY_ID": c.JWTKeyID, "FIELD_ENC_KEY_ID": c.FieldEncKeyID} {
			if strings.TrimSpace(id) != id || id == "" || strings.Contains(strings.ToLower(id), "dev") {
				errs = append(errs, fmt.Errorf("生产环境必须显式设置正式 %s", name))
			}
		}
	}
	return errors.Join(errs...)
}

func get(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func strictBool(name string, defaultValue bool) (bool, error) {
	v, exists := os.LookupEnv(name)
	if !exists {
		return defaultValue, nil
	}
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s 必须为 true 或 false", name)
	}
}

func hexKey(name string, n int) ([]byte, error) {
	v := os.Getenv(name)
	if v == "" {
		return nil, fmt.Errorf("%s 未设置", name)
	}
	b, err := hex.DecodeString(v)
	if err != nil || len(b) != n {
		return nil, fmt.Errorf("%s 必须是 %d 字节的十六进制", name, n)
	}
	return b, nil
}

func allSame(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, x := range b[1:] {
		if x != b[0] {
			return false
		}
	}
	return true
}
