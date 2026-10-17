package egress

import (
	"encoding/json"
	"fmt"
	"strings"
)

// XrayCoreVersion 是客户端内嵌、并做过隔离实测的内核版本。配置结构按它的 infra/conf 实读编写。
const XrayCoreVersion = "26.9.9"

// ProxyTag 等出站标签；客户端与测试都依赖这些固定名字。
const (
	ProxyTag  = "proxy"
	DirectTag = "direct"
	BlockTag  = "block"
	DNSTag    = "dns-out"
	TunTag    = "tun-in"
	SocksTag  = "socks-in"
)

// 私有/保留网段直连，避免把局域网流量送进代理。
var privateCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16",
	"100.64.0.0/10", "224.0.0.0/4", "255.255.255.255/32",
	"::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
}

// 通过出口解析的 DoH：DNS 查询随代理出去，不在本地网络明文泄露。
var remoteDoH = []string{"https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"}

type obj = map[string]any

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (e *Endpoint) tlsSettings(defaultALPN []string) obj {
	t := obj{}
	if sni := e.Params["sni"]; sni != "" {
		t["serverName"] = sni
	}
	if fp := e.Params["fp"]; fp != "" {
		t["fingerprint"] = fp
	}
	if alpn := splitList(e.Params["alpn"]); len(alpn) > 0 {
		t["alpn"] = alpn
	} else if len(defaultALPN) > 0 {
		t["alpn"] = defaultALPN
	}
	if pin := e.Params["pin_sha256"]; pin != "" {
		t["pinnedPeerCertSha256"] = pin
	}
	return t
}

// streamSettings 生成传输与安全层。任何情况下都不写 allowInsecure（Xray 26 已移除该字段）。
func (e *Endpoint) streamSettings() (obj, error) {
	s := obj{}
	network := e.Params["network"]
	switch network {
	case "", "tcp", "raw":
		s["network"] = "raw"
	case "ws":
		s["network"] = "ws"
		ws := obj{"path": defaultPath(e.Params["path"])}
		if h := e.Params["host_header"]; h != "" {
			ws["host"] = h
		}
		s["wsSettings"] = ws
	case "grpc":
		s["network"] = "grpc"
		s["grpcSettings"] = obj{"serviceName": e.Params["service_name"]}
	case "httpupgrade":
		s["network"] = "httpupgrade"
		hu := obj{"path": defaultPath(e.Params["path"])}
		if h := e.Params["host_header"]; h != "" {
			hu["host"] = h
		}
		s["httpupgradeSettings"] = hu
	case "xhttp":
		s["network"] = "xhttp"
		xh := obj{"path": defaultPath(e.Params["path"])}
		if h := e.Params["host_header"]; h != "" {
			xh["host"] = h
		}
		if m := e.Params["mode"]; m != "" {
			xh["mode"] = m
		}
		s["xhttpSettings"] = xh
	default:
		return nil, fmt.Errorf("不支持的传输方式 %q", network)
	}
	switch e.Params["security"] {
	case "", "none":
		s["security"] = "none"
	case "tls":
		s["security"] = "tls"
		s["tlsSettings"] = e.tlsSettings(nil)
	case "reality":
		fp := e.Params["fp"]
		if fp == "" {
			fp = "chrome"
		}
		r := obj{"serverName": e.Params["sni"], "fingerprint": fp, "publicKey": e.Params["pbk"], "shortId": e.Params["sid"]}
		if spx := e.Params["spx"]; spx != "" {
			r["spiderX"] = spx
		}
		s["security"] = "reality"
		s["realitySettings"] = r
	default:
		return nil, fmt.Errorf("不支持的安全层 %q", e.Params["security"])
	}
	return s, nil
}

func defaultPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// Outbound 生成单个 Xray 出站。调用前 Endpoint 必须已通过 Validate。
func (e *Endpoint) Outbound(tag string) (obj, error) {
	if !Supported[e.Protocol] {
		return nil, fmt.Errorf("%s", UnsupportedReason(e.Protocol))
	}
	o := obj{"tag": tag}
	set := obj{"address": e.Host, "port": e.Port}
	switch e.Protocol {
	case SOCKS5, HTTP, HTTPS:
		if e.Protocol == SOCKS5 {
			o["protocol"] = "socks"
		} else {
			o["protocol"] = "http"
		}
		if u := e.Secret["user"]; u != "" {
			set["user"], set["pass"] = u, e.Secret["pass"]
		}
		if e.Protocol == HTTPS {
			o["streamSettings"] = obj{"network": "raw", "security": "tls", "tlsSettings": e.tlsSettings([]string{"http/1.1"})}
		}
	case Shadowsocks:
		o["protocol"] = "shadowsocks"
		set["method"], set["password"] = e.Params["method"], e.Secret["password"]
	case Trojan:
		o["protocol"] = "trojan"
		set["password"] = e.Secret["password"]
	case VLESS:
		o["protocol"] = "vless"
		set["id"], set["encryption"] = e.Secret["uuid"], "none"
		if f := e.Params["flow"]; f != "" {
			set["flow"] = f
		}
	case VMess:
		o["protocol"] = "vmess"
		sec := e.Params["method"]
		if sec == "" {
			sec = "auto"
		}
		set["id"], set["security"] = e.Secret["uuid"], sec
	case Hysteria2:
		o["protocol"] = "hysteria"
		set["version"] = 2
		o["streamSettings"] = obj{"network": "hysteria", "security": "tls", "tlsSettings": e.tlsSettings([]string{"h3"}),
			"hysteriaSettings": obj{"version": 2, "auth": e.Secret["password"]}}
	}
	o["settings"] = set
	switch e.Protocol {
	case Trojan, VLESS, VMess:
		ss, err := e.streamSettings()
		if err != nil {
			return nil, err
		}
		o["streamSettings"] = ss
	}
	return o, nil
}

type ClientMode int

const (
	// ModeTUN 用于安卓：Xray 自带 tun 入站读取 VpnService 交来的 fd。
	ModeTUN ClientMode = iota
	// ModeSOCKS 用于隔离测试：本机 SOCKS 入站替代 TUN，其余配置与 TUN 模式完全一致。
	ModeSOCKS
)

type ClientOptions struct {
	Mode      ClientMode
	SocksPort int // ModeSOCKS 时的监听端口（仅 127.0.0.1）
	MTU       int
	LogLevel  string
	// ProxyPrivate 仅供隔离测试：让私有地址也走出口（测试靶机在 127.0.0.1）。TUN 模式一律拒绝。
	ProxyPrivate bool
}

// ClientConfig 生成客户端完整 Xray 配置（JSON 字节）。这是服务端下发、客户端原样交给内核的唯一形态。
//
// 路由规则顺序即语义：
//  1. 进入 TUN 的 53 端口 UDP 查询 → dns-out，由内核 DNS 通过出口做 DoH，避免本地明文 DNS 泄露；
//  2. 私有/保留网段 → 直连；
//  3. 出口不支持 UDP 时，其余 UDP 一律丢弃（如实表现，而不是让 UDP 从本地网络漏出去）；
//  4. 其余全部 → 出口。
func (e *Endpoint) ClientConfig(o ClientOptions) ([]byte, error) {
	proxy, err := e.Outbound(ProxyTag)
	if err != nil {
		return nil, err
	}
	if o.MTU == 0 {
		o.MTU = 1500
	}
	if o.LogLevel == "" {
		o.LogLevel = "warning"
	}
	sniff := obj{"enabled": true, "destOverride": []string{"http", "tls", "quic"}}
	var inbound obj
	inTag := TunTag
	switch o.Mode {
	case ModeTUN:
		if o.ProxyPrivate {
			return nil, fmt.Errorf("ProxyPrivate 只允许用于测试模式")
		}
		inbound = obj{"tag": TunTag, "port": 0, "protocol": "tun", "settings": obj{"name": "xray0", "mtu": o.MTU}, "sniffing": sniff}
	case ModeSOCKS:
		if o.SocksPort < 1 || o.SocksPort > 65535 {
			return nil, fmt.Errorf("测试模式需要合法的本地端口")
		}
		inTag = SocksTag
		inbound = obj{"tag": SocksTag, "listen": "127.0.0.1", "port": o.SocksPort, "protocol": "socks",
			"settings": obj{"auth": "noauth", "udp": true}, "sniffing": sniff}
	default:
		return nil, fmt.Errorf("未知模式")
	}
	rules := []obj{
		{"type": "field", "inboundTag": []string{inTag}, "network": "udp", "port": "53", "outboundTag": DNSTag},
	}
	if !o.ProxyPrivate {
		rules = append(rules, obj{"type": "field", "ip": privateCIDRs, "outboundTag": DirectTag})
	}
	if !e.SupportsUDP {
		rules = append(rules, obj{"type": "field", "network": "udp", "outboundTag": BlockTag})
	}
	rules = append(rules, obj{"type": "field", "network": "tcp,udp", "outboundTag": ProxyTag})
	cfg := obj{
		"log":      obj{"loglevel": o.LogLevel},
		"dns":      obj{"servers": remoteDoH, "queryStrategy": "UseIPv4"},
		"inbounds": []obj{inbound},
		"outbounds": []obj{proxy,
			{"tag": DirectTag, "protocol": "freedom"},
			{"tag": BlockTag, "protocol": "blackhole"},
			{"tag": DNSTag, "protocol": "dns"},
		},
		"routing": obj{"domainStrategy": "AsIs", "rules": rules},
		// 出站流量计数：客户端只用来在界面显示本次连接的上下行，不作为计费依据。
		"stats":  obj{},
		"policy": obj{"system": obj{"statsOutboundUplink": true, "statsOutboundDownlink": true}},
	}
	return json.Marshal(cfg)
}
