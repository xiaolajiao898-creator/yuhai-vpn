package egress

import (
	"encoding/base64"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

func mustOne(t *testing.T, in string, o ParseOptions) *Endpoint {
	t.Helper()
	rows, err := Parse(in, o)
	if err != nil {
		t.Fatalf("parse %q: %v", in, err)
	}
	if len(rows) != 1 || rows[0].Endpoint == nil {
		t.Fatalf("parse %q: rows=%+v", in, rows)
	}
	return rows[0].Endpoint
}

func mustErr(t *testing.T, in string, o ParseOptions, want string) {
	t.Helper()
	rows, err := Parse(in, o)
	if err != nil {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("parse %q: err %v, want %q", in, err, want)
		}
		return
	}
	if len(rows) != 1 || rows[0].Endpoint != nil || !strings.Contains(rows[0].Error, want) {
		t.Fatalf("parse %q: rows=%+v want error %q", in, rows, want)
	}
}

func TestColonFormats(t *testing.T) {
	e := mustOne(t, "1.2.3.4:1080:alice:p@ss:word", ParseOptions{DefaultProtocol: SOCKS5, DefaultUDP: true, CountryCode: "jp"})
	if e.Protocol != SOCKS5 || e.Host != "1.2.3.4" || e.Port != 1080 || e.Secret["user"] != "alice" || e.Secret["pass"] != "p@ss:word" || !e.SupportsUDP || e.CountryCode != "JP" {
		t.Fatalf("got %+v %v", e, e.Secret)
	}
	e = mustOne(t, "bob:secret@proxy.example.com:3128", ParseOptions{DefaultProtocol: HTTP, DefaultUDP: true})
	if e.Protocol != HTTP || e.Host != "proxy.example.com" || e.Port != 3128 || e.Secret["user"] != "bob" || e.SupportsUDP {
		t.Fatalf("http got %+v", e)
	}
	e = mustOne(t, "[2001:db8::1]:8080", ParseOptions{DefaultProtocol: SOCKS5})
	if e.Host != "2001:db8::1" || e.Port != 8080 || len(e.Secret) != 0 {
		t.Fatalf("v6 got %+v", e)
	}
	e = mustOne(t, "gw.example.net:443:u:p", ParseOptions{DefaultProtocol: HTTPS})
	if e.Protocol != HTTPS || e.Params["security"] != "tls" || e.Params["sni"] != "gw.example.net" {
		t.Fatalf("https got %+v", e.Params)
	}
	mustErr(t, "1.2.3.4", ParseOptions{}, "缺少端口")
	mustErr(t, "1.2.3.4:99999", ParseOptions{}, "端口无效")
	mustErr(t, "1.2.3.4:80:onlyuser", ParseOptions{}, "IP:端口:账号:密码")
	mustErr(t, "bad host!:80", ParseOptions{}, "主机")
}

func TestURIs(t *testing.T) {
	e := mustOne(t, "socks5://u1:p1@5.6.7.8:1080?udp=1#Tokyo%201", ParseOptions{})
	if e.Protocol != SOCKS5 || !e.SupportsUDP || e.Name != "Tokyo 1" || e.Secret["pass"] != "p1" {
		t.Fatalf("socks got %+v", e)
	}
	b := base64.StdEncoding.EncodeToString([]byte("u2:p2"))
	e = mustOne(t, "socks://"+b+"@5.6.7.8:1081#x", ParseOptions{})
	if e.Secret["user"] != "u2" || e.Secret["pass"] != "p2" {
		t.Fatalf("v2rayN socks got %v", e.Secret)
	}
	e = mustOne(t, "https://u:p@secure.example.org:8443", ParseOptions{})
	if e.Protocol != HTTPS || e.Params["sni"] != "secure.example.org" {
		t.Fatalf("https uri %+v", e.Params)
	}
	e = mustOne(t, "trojan://pw@t.example.com:443?sni=cdn.example.com&type=ws&path=%2Fws&host=cdn.example.com#tr", ParseOptions{})
	if e.Protocol != Trojan || e.Params["network"] != "ws" || e.Params["path"] != "/ws" || e.Params["sni"] != "cdn.example.com" || e.Secret["password"] != "pw" {
		t.Fatalf("trojan %+v", e.Params)
	}
	uuid := "0b5c1f6e-2d1c-4e8b-9a8b-1c2d3e4f5a6b"
	pbk := strings.Repeat("A", 43)
	e = mustOne(t, "vless://"+uuid+"@9.9.9.9:443?encryption=none&security=reality&sni=www.microsoft.com&fp=chrome&pbk="+pbk+"&sid=abcd&flow=xtls-rprx-vision&type=tcp#r", ParseOptions{})
	if e.Protocol != VLESS || e.Params["security"] != "reality" || e.Params["flow"] != "xtls-rprx-vision" || e.Params["network"] != "" {
		t.Fatalf("vless %+v", e.Params)
	}
	vm, _ := json.Marshal(map[string]any{"v": "2", "ps": "vm", "add": "vm.example.com", "port": "443", "id": uuid, "aid": "0", "scy": "auto", "net": "ws", "host": "vm.example.com", "path": "/v", "tls": "tls"})
	e = mustOne(t, "vmess://"+base64.StdEncoding.EncodeToString(vm), ParseOptions{})
	if e.Protocol != VMess || e.Params["network"] != "ws" || e.Params["security"] != "tls" || e.Params["sni"] != "vm.example.com" {
		t.Fatalf("vmess %+v", e.Params)
	}
	ss := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:sspw"))
	e = mustOne(t, "ss://"+ss+"@1.1.1.2:8388#ss1", ParseOptions{})
	if e.Protocol != Shadowsocks || e.Params["method"] != "aes-256-gcm" || e.Secret["password"] != "sspw" || !e.SupportsUDP {
		t.Fatalf("ss %+v", e)
	}
	e = mustOne(t, "ss://2022-blake3-aes-128-gcm:"+"AAAAAAAAAAAAAAAAAAAAAA%3D%3D"+"@1.1.1.3:8389#ss2022", ParseOptions{})
	if e.Params["method"] != "2022-blake3-aes-128-gcm" || e.Secret["password"] != "AAAAAAAAAAAAAAAAAAAAAA==" {
		t.Fatalf("ss2022 %+v %v", e.Params, e.Secret)
	}
	legacy := base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:pw3@1.1.1.4:8390"))
	e = mustOne(t, "ss://"+legacy+"#old", ParseOptions{})
	if e.Host != "1.1.1.4" || e.Port != 8390 {
		t.Fatalf("legacy ss %+v", e)
	}
	e = mustOne(t, "hysteria2://authpw@hy.example.com:8443/?sni=hy.example.com#h", ParseOptions{})
	if e.Protocol != Hysteria2 || e.Secret["password"] != "authpw" || e.Params["security"] != "tls" {
		t.Fatalf("hy2 %+v", e)
	}
	mustOne(t, "hy2://a:b@hy.example.com:443", ParseOptions{})
}

func TestRejections(t *testing.T) {
	uuid := "0b5c1f6e-2d1c-4e8b-9a8b-1c2d3e4f5a6b"
	mustErr(t, "trojan://pw@t.example.com:443?allowInsecure=1", ParseOptions{}, "TLS")
	mustErr(t, "hysteria2://pw@h.example.com:443?insecure=1", ParseOptions{}, "TLS")
	mustErr(t, "vless://"+uuid+"@h.example.com:443?security=tls&allowInsecure=true", ParseOptions{}, "TLS")
	vm, _ := json.Marshal(map[string]any{"add": "a.example.com", "port": 443, "id": uuid, "tls": "tls", "allowInsecure": true})
	mustErr(t, "vmess://"+base64.StdEncoding.EncodeToString(vm), ParseOptions{}, "TLS")
	mustErr(t, "tuic://"+uuid+":pw@h.example.com:443", ParseOptions{}, "TUIC")
	mustErr(t, "ss://"+base64.RawURLEncoding.EncodeToString([]byte("rc4-md5:pw"))+"@1.1.1.1:1", ParseOptions{}, "不安全")
	mustErr(t, "ss://"+base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:pw"))+"@1.1.1.1:1?plugin=v2ray-plugin", ParseOptions{}, "插件")
	mustErr(t, "vless://not-a-uuid@h.example.com:443", ParseOptions{}, "UUID")
	mustErr(t, "vless://"+uuid+"@h.example.com:443?security=reality&sni=x.com&pbk=short", ParseOptions{}, "Reality")
	mustErr(t, "hysteria2://pw@h.example.com:443?obfs=salamander", ParseOptions{}, "obfs")
	mustErr(t, "gopher://x@h.example.com:70", ParseOptions{}, "无法识别")
	mustErr(t, "http://u@h.example.com:80", ParseOptions{}, "同时提供")
	mustErr(t, "a:b:c:d", ParseOptions{DefaultProtocol: "VLESS"}, "默认协议")
}

func TestJSONAndCSVAndSubscription(t *testing.T) {
	js := `[{"protocol":"socks5","host":"2.2.2.2","port":1080,"username":"u","password":"p","name":"n1","country_code":"sg","supports_udp":true},
	        "http://x:y@3.3.3.3:8080",
	        {"type":"trojan","server":"t.example.com","port":"443","password":"z","params":{"sni":"t.example.com"}},
	        {"protocol":"socks5","host":"2.2.2.2","port":1080,"username":"u","password":"p"},
	        {"protocol":"socks5","host":"2.2.2.2","port":1080,"bogus":1}]`
	rows, err := Parse(js, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].Endpoint == nil || rows[0].Endpoint.CountryCode != "SG" || !rows[0].Endpoint.SupportsUDP ||
		rows[1].Endpoint == nil || rows[2].Endpoint == nil || rows[2].Endpoint.Protocol != Trojan ||
		!rows[3].Duplicate || rows[4].Error == "" {
		t.Fatalf("json rows %+v", rows)
	}
	csvText := "protocol,host,port,username,password,country_code,city\nsocks5,4.4.4.4,1080,a,b,KR,Seoul\nhttp,5.5.5.5,3128,,,US,LA\nsocks5,bad host,1,a,b,,\n"
	rows, err = Parse(csvText, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Endpoint == nil || rows[0].Endpoint.City != "Seoul" || rows[1].Endpoint == nil || rows[2].Error == "" {
		t.Fatalf("csv rows %+v", rows)
	}
	sub := base64.StdEncoding.EncodeToString([]byte("socks5://a:b@6.6.6.6:1080\ntrojan://pw@t2.example.com:443\n"))
	rows, err = Parse(sub, ParseOptions{})
	if err != nil || len(rows) != 2 || rows[0].Endpoint == nil || rows[1].Endpoint == nil {
		t.Fatalf("sub rows %+v %v", rows, err)
	}
	if _, err := Parse("protocol,host,port,evil\nsocks5,1.1.1.1,1,x", ParseOptions{}); err == nil {
		t.Fatal("unknown csv column accepted")
	}
}

func TestPreviewNeverEchoesSecrets(t *testing.T) {
	in := "1.2.3.4:1080:alice:TOPSECRETPW\nsocks5://bob:ANOTHERSECRET@5.6.7.8:1080\nbroken:line:with:SECRETX:y:z\ntrojan://TROJANSECRET@h.example.com:443?allowInsecure=1"
	rows, err := Parse(in, ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(rows)
	for _, s := range []string{"TOPSECRETPW", "ANOTHERSECRET", "SECRETX", "TROJANSECRET", "alice", "bob"} {
		if strings.Contains(string(blob), s) {
			t.Fatalf("preview leaked %q: %s", s, blob)
		}
	}
}

func TestFingerprintDistinguishesAccounts(t *testing.T) {
	a := mustOne(t, "1.2.3.4:1080:u1:p", ParseOptions{})
	b := mustOne(t, "1.2.3.4:1080:u2:p", ParseOptions{})
	c := mustOne(t, "1.2.3.4:1080:u1:other", ParseOptions{})
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different accounts must not dedupe")
	}
	if a.Fingerprint() != c.Fingerprint() {
		t.Fatal("same account with new password should dedupe (update, not duplicate)")
	}
}

func TestSubscriptionURLValidation(t *testing.T) {
	for _, bad := range []string{"http://sub.example.com/x", "https://127.0.0.1/x", "https://10.1.2.3/x", "https://[::1]/x",
		"https://localhost/x", "https://u:p@sub.example.com/x", "https://sub.example.com:8080/x", "https://169.254.169.254/latest", "file:///etc/passwd",
		"https://printer.local/x", "https://100.64.1.1/"} {
		if _, err := ValidateSubscriptionURL(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, err := ValidateSubscriptionURL("https://sub.example.com/api/v1/client?token=abc"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Fatalf("%s should be public", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "::1", "fe80::1", "::ffff:127.0.0.1", "0.0.0.0", "169.254.169.254", "fd00::1"} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Fatalf("%s should be blocked", s)
		}
	}
}

func TestImportRejectsPrivateHosts(t *testing.T) {
	for _, line := range []string{"127.0.0.1:1080:u:p", "10.1.2.3:1080", "socks5://u:p@192.168.1.5:1080", "http://u:p@localhost:3128",
		"trojan://pw@proxy.local:443", "socks5://u:p@[::1]:1080", "socks5://u:p@[fd00::1]:1080", "socks5://u:p@100.64.0.1:1080"} {
		rows, err := Parse(line, ParseOptions{DefaultProtocol: SOCKS5})
		if err != nil || len(rows) != 1 || rows[0].Endpoint != nil || rows[0].Error == "" {
			t.Fatalf("%s 应被拒绝：%v %+v", line, err, rows)
		}
		rows, _ = Parse(line, ParseOptions{DefaultProtocol: SOCKS5, AllowPrivate: true})
		if rows[0].Endpoint == nil && !strings.Contains(line, "localhost") {
			t.Fatalf("%s 在测试模式下应可解析：%+v", line, rows)
		}
	}
}
