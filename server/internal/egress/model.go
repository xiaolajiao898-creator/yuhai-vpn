// Package egress 管理「出口」：外购代理（SOCKS5/HTTP/HTTPS 等）与自建多协议节点的统一登记、
// 导入解析、脱敏展示与客户端连接配置生成。
//
// 边界（与节点控制面分开写死）：
//
//  1. 香港后台只做账号、套餐与配置下发，**从不转发上网流量**。客户端拿到出口配置后直连出口。
//  2. 凭据（用户名/密码/UUID/口令）只以字段加密形式落库；列表、日志、审计、预览一律脱敏，
//     只有向「有权益的设备」下发连接配置那一刻才解密。
//  3. TLS 校验不能被全局关闭：任何导入内容里的 allowInsecure/insecure 都会被拒绝，
//     唯一允许的放宽是「按条目固定证书指纹」（pinnedPeerCertSha256），那是更严而不是更松。
//  4. 协议能力如实：HTTP/HTTPS 代理没有 UDP；SOCKS5 的 UDP 取决于代理端，由管理员标注。
//     不支持的协议（TUIC 等）在导入时逐行报错，而不是静默丢弃或编造字段。
package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 协议代码。与数据库 CHECK、OpenAPI 枚举、客户端三处一致。
const (
	SOCKS5      = "SOCKS5"
	HTTP        = "HTTP"
	HTTPS       = "HTTPS"
	Shadowsocks = "SHADOWSOCKS"
	Trojan      = "TROJAN"
	VLESS       = "VLESS"
	VMess       = "VMESS"
	Hysteria2   = "HYSTERIA2"
	TUIC        = "TUIC"
)

// Protocols 是可登记的全部协议；Supported 是当前内核（Xray-core）真正能连的协议。
var Protocols = []string{SOCKS5, HTTP, HTTPS, Shadowsocks, Trojan, VLESS, VMess, Hysteria2, TUIC}

var Supported = map[string]bool{
	SOCKS5: true, HTTP: true, HTTPS: true, Shadowsocks: true, Trojan: true,
	VLESS: true, VMess: true, Hysteria2: true,
}

// UnsupportedReason 解释为什么某协议不能用（导入逐行报错用）。
func UnsupportedReason(protocol string) string {
	switch protocol {
	case TUIC:
		return "TUIC 当前内核（Xray-core）不支持，需另行评估 sing-box（GPLv3）"
	case "":
		return "无法识别协议"
	}
	return "协议 " + protocol + " 暂不支持"
}

// Endpoint 是一条出口的完整描述（解析后、落库前）。Secret 里的字段永远不出现在列表与日志中。
type Endpoint struct {
	Protocol    string            `json:"protocol"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	Name        string            `json:"name"`
	CountryCode string            `json:"country_code"`
	City        string            `json:"city"`
	Region      string            `json:"region"`
	SupportsUDP bool              `json:"supports_udp"`
	Params      map[string]string `json:"params"` // 非机密：sni、alpn、transport、path、reality 公钥等
	Secret      map[string]string `json:"-"`      // 机密：user/pass/uuid/password
}

// 允许的非机密参数键。白名单之外一律拒绝，避免把未知（可能不安全的）选项带进客户端配置。
var paramKeys = map[string]bool{
	"sni": true, "alpn": true, "fp": true, "security": true, // tls | reality | none
	"network": true, // tcp | ws | grpc | httpupgrade | xhttp
	"path":    true, "host_header": true, "service_name": true, "mode": true,
	"flow": true, "pbk": true, "sid": true, "spx": true,
	"method":      true, // shadowsocks cipher / vmess security
	"pin_sha256":  true, // 按条目固定证书指纹（十六进制 SHA-256）
	"obfs":        true, // hysteria2 obfs 类型（当前仅接受空）
	"source_kind": true, // THIRD_PARTY | SELF_HOSTED
}

// 机密字段白名单。
var secretKeys = map[string]bool{"user": true, "pass": true, "uuid": true, "password": true}

var (
	hostnameRE = regexp.MustCompile(`^(?i)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	countryRE  = regexp.MustCompile(`^[A-Z]{2}$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hex64RE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	shortIDRE  = regexp.MustCompile(`^[0-9a-f]{0,16}$`)
	pbkRE      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// SS 加密方式白名单（AEAD 与 2022）。老旧流加密一律拒绝。
var ssMethods = map[string]bool{
	"aes-128-gcm": true, "aes-256-gcm": true, "chacha20-ietf-poly1305": true, "chacha20-poly1305": true,
	"xchacha20-ietf-poly1305": true, "xchacha20-poly1305": true,
	"2022-blake3-aes-128-gcm": true, "2022-blake3-aes-256-gcm": true, "2022-blake3-chacha20-poly1305": true,
}

var vmessSecurities = map[string]bool{"": true, "auto": true, "aes-128-gcm": true, "chacha20-poly1305": true, "none": true, "zero": true}

var networks = map[string]bool{"": true, "tcp": true, "raw": true, "ws": true, "grpc": true, "httpupgrade": true, "xhttp": true}

var fingerprints = map[string]bool{"": true, "chrome": true, "firefox": true, "safari": true, "ios": true, "android": true, "edge": true, "360": true, "qq": true, "random": true, "randomized": true}

var flows = map[string]bool{"": true, "xtls-rprx-vision": true}

func printable(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// NormalizeHost 小写化并校验主机：IP 字面量或合法域名。拒绝空白、端口、路径等夹带。
func NormalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if h == "" || len(h) > 253 {
		return "", fmt.Errorf("主机为空或过长")
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		if ip.Zone() != "" {
			return "", fmt.Errorf("主机不能带网卡区域")
		}
		return ip.Unmap().String(), nil
	}
	if !hostnameRE.MatchString(h) {
		return "", fmt.Errorf("主机不是合法的 IP 或域名")
	}
	return strings.ToLower(h), nil
}

// Validate 统一校验（解析与手工录入走同一份）。返回的错误信息面向管理员，不含机密。
func (e *Endpoint) Validate() error {
	if e.Params == nil {
		e.Params = map[string]string{}
	}
	if e.Secret == nil {
		e.Secret = map[string]string{}
	}
	known := false
	for _, p := range Protocols {
		if p == e.Protocol {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("无法识别协议")
	}
	if !Supported[e.Protocol] {
		return fmt.Errorf("%s", UnsupportedReason(e.Protocol))
	}
	host, err := NormalizeHost(e.Host)
	if err != nil {
		return err
	}
	e.Host = host
	if e.Port < 1 || e.Port > 65535 {
		return fmt.Errorf("端口必须在 1–65535")
	}
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		e.Name = e.Protocol + " " + e.Host
	}
	if !printable(e.Name, 64) {
		e.Name = string([]rune(e.Name)[:min(64, utf8.RuneCountInString(e.Name))])
		if !printable(e.Name, 64) {
			return fmt.Errorf("名称含不可见字符")
		}
	}
	e.CountryCode = strings.ToUpper(strings.TrimSpace(e.CountryCode))
	if e.CountryCode != "" && !countryRE.MatchString(e.CountryCode) {
		return fmt.Errorf("国家代码必须是两位字母")
	}
	if !printable(e.City, 64) || !printable(e.Region, 32) {
		return fmt.Errorf("城市或地区含不可见字符或过长")
	}
	for k, v := range e.Params {
		if !paramKeys[k] {
			return fmt.Errorf("不支持的参数 %q", k)
		}
		if !printable(v, 512) {
			return fmt.Errorf("参数 %s 含不可见字符或过长", k)
		}
		if v == "" {
			delete(e.Params, k)
		}
	}
	for k, v := range e.Secret {
		if !secretKeys[k] {
			return fmt.Errorf("不支持的凭据字段")
		}
		if !printable(v, 512) {
			return fmt.Errorf("凭据含不可见字符或过长")
		}
		if v == "" {
			delete(e.Secret, k)
		}
	}
	if pin := e.Params["pin_sha256"]; pin != "" {
		pin = strings.ToLower(strings.ReplaceAll(pin, ":", ""))
		if !hex64RE.MatchString(pin) {
			return fmt.Errorf("证书指纹必须是 64 位十六进制 SHA-256")
		}
		e.Params["pin_sha256"] = pin
	}
	if fp := e.Params["fp"]; !fingerprints[fp] {
		return fmt.Errorf("不支持的 TLS 指纹 %q", fp)
	}
	if n := e.Params["network"]; !networks[n] {
		return fmt.Errorf("不支持的传输方式 %q", n)
	}
	switch e.Protocol {
	case SOCKS5, HTTP, HTTPS:
		if (e.Secret["user"] == "") != (e.Secret["pass"] == "") {
			return fmt.Errorf("用户名与密码必须同时提供")
		}
		if e.Protocol != HTTPS && (e.Params["security"] != "" || e.Params["sni"] != "") {
			return fmt.Errorf("%s 代理不能设置 TLS 参数（需要 TLS 请用 HTTPS 类型）", e.Protocol)
		}
		if e.Params["network"] != "" && e.Params["network"] != "tcp" {
			return fmt.Errorf("%s 代理只支持 TCP 传输", e.Protocol)
		}
		if e.Protocol != SOCKS5 {
			e.SupportsUDP = false // HTTP CONNECT 没有 UDP，如实标注
		}
	case Shadowsocks:
		if !ssMethods[e.Params["method"]] {
			return fmt.Errorf("Shadowsocks 加密方式不支持或不安全")
		}
		if e.Secret["password"] == "" {
			return fmt.Errorf("Shadowsocks 缺少密码")
		}
		if e.Params["security"] != "" || e.Params["network"] != "" {
			return fmt.Errorf("Shadowsocks 不支持附加传输/TLS 插件")
		}
		e.SupportsUDP = true
	case Trojan:
		if e.Secret["password"] == "" {
			return fmt.Errorf("Trojan 缺少密码")
		}
		if s := e.Params["security"]; s == "" || s == "none" {
			e.Params["security"] = "tls" // Trojan 必须 TLS
		} else if s != "tls" {
			return fmt.Errorf("Trojan 只支持 TLS")
		}
		e.SupportsUDP = true
	case VLESS:
		if !uuidRE.MatchString(e.Secret["uuid"]) {
			return fmt.Errorf("VLESS 缺少合法 UUID")
		}
		switch e.Params["security"] {
		case "tls", "none", "":
			if e.Params["security"] == "" {
				e.Params["security"] = "none"
			}
		case "reality":
			if !pbkRE.MatchString(e.Params["pbk"]) || !shortIDRE.MatchString(e.Params["sid"]) || e.Params["sni"] == "" {
				return fmt.Errorf("Reality 需要合法的 pbk、sid 与 sni")
			}
		default:
			return fmt.Errorf("VLESS 安全层只支持 tls / reality / none")
		}
		if !flows[e.Params["flow"]] {
			return fmt.Errorf("不支持的 flow")
		}
		e.SupportsUDP = true
	case VMess:
		if !uuidRE.MatchString(e.Secret["uuid"]) {
			return fmt.Errorf("VMess 缺少合法 UUID")
		}
		if !vmessSecurities[e.Params["method"]] {
			return fmt.Errorf("VMess 加密方式不支持")
		}
		if s := e.Params["security"]; s != "" && s != "tls" && s != "none" {
			return fmt.Errorf("VMess 安全层只支持 tls / none")
		}
		e.SupportsUDP = true
	case Hysteria2:
		if e.Secret["password"] == "" {
			return fmt.Errorf("Hysteria2 缺少认证口令")
		}
		if e.Params["obfs"] != "" {
			return fmt.Errorf("Hysteria2 的 obfs 混淆暂不支持")
		}
		e.Params["security"] = "tls"
		delete(e.Params, "network")
		e.SupportsUDP = true
	}
	return nil
}

// Fingerprint 用于去重：同协议、同主机端口、同身份（用户名/UUID/口令哈希）视为同一条。
// 口令只以哈希参与，指纹本身不可逆出凭据。
func (e *Endpoint) Fingerprint() string {
	id := e.Secret["user"] + "\x00" + e.Secret["uuid"]
	if id == "\x00" {
		sum := sha256.Sum256([]byte(e.Secret["password"]))
		id = hex.EncodeToString(sum[:8])
	}
	sum := sha256.Sum256([]byte(e.Protocol + "|" + strings.ToLower(e.Host) + "|" + fmt.Sprint(e.Port) + "|" + id))
	return hex.EncodeToString(sum[:])
}

// MaskedIdentity 返回可展示的身份提示（如 "ab***"），从不包含密码。
func (e *Endpoint) MaskedIdentity() string {
	for _, k := range []string{"user", "uuid"} {
		if v := e.Secret[k]; v != "" {
			return MaskSecret(v)
		}
	}
	if e.Secret["password"] != "" {
		return "******"
	}
	return ""
}

// MaskSecret 只保留前 2 个字符。
func MaskSecret(v string) string {
	r := []rune(v)
	if len(r) <= 2 {
		return "***"
	}
	return string(r[:2]) + "***"
}
