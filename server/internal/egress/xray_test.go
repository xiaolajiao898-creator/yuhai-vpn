package egress

import (
	"encoding/json"
	"strings"
	"testing"
)

func one(t *testing.T, line string) *Endpoint {
	t.Helper()
	rows, err := Parse(line, ParseOptions{DefaultProtocol: SOCKS5})
	if err != nil || len(rows) != 1 || rows[0].Endpoint == nil {
		t.Fatalf("parse %q: %v %+v", line, err, rows)
	}
	return rows[0].Endpoint
}

func TestProxyPrivateOnlyInTestMode(t *testing.T) {
	e := one(t, "socks5://u:p@203.0.113.1:1080")
	if _, err := e.ClientConfig(ClientOptions{Mode: ModeTUN, ProxyPrivate: true}); err == nil {
		t.Fatal("TUN 模式不应接受 ProxyPrivate")
	}
	tun, _ := e.ClientConfig(ClientOptions{Mode: ModeTUN})
	if !strings.Contains(string(tun), `"outboundTag":"direct"`) {
		t.Fatal("TUN 模式缺少私有网段直连规则")
	}
	test, err := e.ClientConfig(ClientOptions{Mode: ModeSOCKS, SocksPort: 10808, ProxyPrivate: true})
	if err != nil || strings.Contains(string(test), `"outboundTag":"direct"`) {
		t.Fatalf("测试模式私有网段应走出口：%v %s", err, test)
	}
}

// 每种协议生成的配置都必须是合法 JSON、从不含 allowInsecure，且自签证书只能以指纹钉住的方式放行。
func TestEveryProtocolConfigShape(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	cases := map[string][]string{
		"socks5://u:p@203.0.113.1:1080":                                                               {`"protocol":"socks"`},
		"http://u:p@203.0.113.1:3128":                                                                 {`"protocol":"http"`},
		"https://u:p@example.com:443?pin_sha256=" + pin:                                               {`"security":"tls"`, pin},
		"ss://aes-256-gcm:pw@203.0.113.1:8388":                                                        {`"method":"aes-256-gcm"`},
		"trojan://pw@example.com:443?sni=example.com":                                                 {`"protocol":"trojan"`, `"serverName":"example.com"`},
		"vless://11111111-1111-4111-8111-111111111111@example.com:443?security=tls&type=ws&path=%2Fw": {`"network":"ws"`},
		"hysteria2://auth@example.com:443?sni=example.com":                                            {`"protocol":"hysteria"`},
	}
	for line, wants := range cases {
		e := one(t, line)
		cfg, err := e.ClientConfig(ClientOptions{Mode: ModeTUN})
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if !json.Valid(cfg) {
			t.Fatalf("%s: 非法 JSON", line)
		}
		low := strings.ToLower(string(cfg))
		if strings.Contains(low, "allowinsecure") {
			t.Fatalf("%s: 出现 allowInsecure", line)
		}
		for _, w := range wants {
			if !strings.Contains(string(cfg), w) {
				t.Fatalf("%s: 缺少 %s：%s", line, w, cfg)
			}
		}
	}
}
