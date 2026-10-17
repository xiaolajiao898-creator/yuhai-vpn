package httpx

import (
	"net/netip"
	"os"
	"strings"
)

// TrustedProxiesFromEnv 读取 TRUSTED_PROXIES（逗号分隔的 IP 或 CIDR）。
// 未设置时返回 nil：只认直连地址，任何转发头都不能改变限流身份。
// 生产环境由本机反向代理（Caddy）转发时应设为 127.0.0.1,::1，
// 否则所有用户都会被当成同一个 127.0.0.1 共用限流额度。
// 非法条目直接丢弃，绝不因为配置写错而退化成「信任所有来源」。
func TrustedProxiesFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			if p.Bits() == 0 {
				continue
			}
			out = append(out, p.String())
			continue
		}
		if a, err := netip.ParseAddr(item); err == nil {
			out = append(out, a.String())
		}
	}
	return out
}
