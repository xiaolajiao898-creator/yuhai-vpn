package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// 订阅抓取的防护（SSRF）：
//
//   - 只允许 https，端口只允许 443 或 1024 以上的非常见内网端口以外的公网端口；
//   - 每一次**真正建立 TCP 连接**时再校验目标 IP（net.Dialer.Control），防止 DNS 重绑定：
//     解析时是公网、连接时变内网的情况照样被拒；
//   - 拒绝一切重定向（重定向是绕过校验最常见的办法）；
//   - 不带 Cookie、不带任何后台凭据或自定义头，固定 UA；
//   - 限时 15 秒、限大小 2MB。
const (
	FetchTimeout  = 15 * time.Second
	FetchMaxBytes = 2 << 20
)

var ErrBlockedAddress = errors.New("订阅地址指向内网、本机或保留地址，已拒绝")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "100::/64", "2001::/32", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicAddr 判断一个 IP 是否可作为订阅抓取目标。
// PublicHost 判断出口主机是否可能是公网地址：只挡住一眼可见、用户设备必然连不上的本机/内网地址
// （回环、私有、链路本地、未指定、组播、运营商级 NAT、IPv6 ULA）以及本机/局域网域名后缀。
// 与订阅抓取用的 PublicAddr 不同，这里不挡文档保留段等：它是录入纠错，不是 SSRF 防线。
// 域名解析结果由客户端决定，这里不做解析。
func PublicHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip))
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suf := range []string{"localhost", ".localhost", ".local", ".internal", ".lan", ".home.arpa"} {
		if h == strings.TrimPrefix(suf, ".") || strings.HasSuffix(h, suf) {
			return false
		}
	}
	return true
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateSubscriptionURL 做静态校验（不联网）：https、无用户信息、主机合法、非字面内网 IP。
func ValidateSubscriptionURL(raw string) (*url.URL, error) {
	if len(raw) > 2048 || !printable(raw, 2048) {
		return nil, errors.New("订阅地址过长或含不可见字符")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return nil, errors.New("订阅地址必须是 https:// 开头的完整链接")
	}
	if u.User != nil {
		return nil, errors.New("订阅地址不能包含账号密码（user@host）")
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicAddr(ip) {
			return nil, ErrBlockedAddress
		}
	} else if _, err := NormalizeHost(host); err != nil || strings.HasSuffix(strings.ToLower(host), ".local") ||
		strings.HasSuffix(strings.ToLower(host), ".internal") || strings.EqualFold(host, "localhost") {
		return nil, ErrBlockedAddress
	}
	if p := u.Port(); p != "" && p != "443" {
		return nil, errors.New("订阅地址只允许默认 https 端口 443")
	}
	return u, nil
}

// Fetcher 可替换 Dialer 以便测试；生产使用 NewFetcher()。
type Fetcher struct {
	Client *http.Client
}

func NewFetcher() *Fetcher {
	dialer := &net.Dialer{
		Timeout: 8 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !PublicAddr(ap.Addr()) || ap.Port() != 443 {
				return ErrBlockedAddress
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                  nil, // 不经任何环境代理，避免把请求送到内网代理
		DialContext:            dialer.DialContext,
		TLSHandshakeTimeout:    8 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      true,
	}
	return &Fetcher{Client: &http.Client{
		Transport: tr,
		Timeout:   FetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("订阅地址发生重定向，已拒绝（请填写最终地址）")
		},
	}}
}

// Fetch 抓取订阅内容。返回的错误信息不含订阅地址本身（地址里常带供应商令牌）。
func (f *Fetcher) Fetch(ctx context.Context, raw string) (string, error) {
	u, err := ValidateSubscriptionURL(raw)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("订阅地址无效")
	}
	req.Header.Set("User-Agent", "YuhaiSubscription/1.0")
	req.Header.Set("Accept", "text/plain, application/json, */*")
	resp, err := f.Client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlockedAddress) || strings.Contains(err.Error(), ErrBlockedAddress.Error()) {
			return "", ErrBlockedAddress
		}
		if strings.Contains(err.Error(), "重定向") {
			return "", errors.New("订阅地址发生重定向，已拒绝（请填写最终地址）")
		}
		return "", errors.New("订阅抓取失败（网络错误或超时）")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("订阅服务器返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, FetchMaxBytes+1))
	if err != nil {
		return "", errors.New("订阅内容读取失败")
	}
	if len(body) > FetchMaxBytes {
		return "", errors.New("订阅内容超过 2MB")
	}
	return string(body), nil
}
