package connect

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuhai-vpn/server/internal/egress"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 出口下发的测试重点与 WireGuard 同一套拒绝面，外加两条本域特有的：
// 列表绝不含主机与凭据；配置里 TLS 校验从不关闭，不支持 UDP 的出口 UDP 被丢弃。

func testCipher(t *testing.T) *fieldenc.Cipher {
	t.Helper()
	c, err := fieldenc.New("fek-test", []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) egress(t *testing.T, c *fieldenc.Cipher, id int64, line string, status string, minTier int) {
	t.Helper()
	rows, err := egress.Parse(line, egress.ParseOptions{DefaultProtocol: egress.SOCKS5})
	if err != nil || len(rows) != 1 || rows[0].Endpoint == nil {
		t.Fatalf("fixture parse: %v %+v", err, rows)
	}
	e := rows[0].Endpoint
	params, _ := json.Marshal(e.Params)
	sealed, err := egress.SealSecret(c, id, e.Secret)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO egress_endpoints(id,name,protocol,host,port,country_code,supports_udp,params,secret_enc,identity_hint,
 fingerprint,min_tier,status,source,created_at,updated_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,'JP',$6,$7,$8,$9,$10,$11,$12,'IMPORT',$13,$13)`,
		id, e.Name, e.Protocol, e.Host, e.Port, e.SupportsUDP, params, sealed, e.MaskedIdentity(), e.Fingerprint(), minTier, status, f.clock.now)
}

func TestEgressListNeverLeaksHostsOrSecretsIntegration(t *testing.T) {
	f := newFixture(t)
	c := testCipher(t)
	f.svc.Cipher = c
	f.plan(t, 1, nil, 0)
	f.egress(t, c, 1, "socks5://alice:SOCKSPASSWORD@203.0.113.10:1080#东京住宅", "ENABLED", 1)
	f.egress(t, c, 2, "http://bob:HTTPPASSWORD@203.0.113.11:3128#高级", "ENABLED", 3)
	f.egress(t, c, 3, "socks5://carol:x@203.0.113.12:1080#停用", "DISABLED", 1)
	items, err := f.svc.EgressList(f.ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "e1" || !items[0].Available || items[1].ID != "e2" || items[1].Available ||
		items[1].LockedReason == nil || *items[1].LockedReason != "TIER" || items[1].SupportsUDP {
		t.Fatalf("list %+v", items)
	}
	blob, _ := json.Marshal(items)
	for _, s := range []string{"203.0.113", "alice", "bob", "SOCKSPASSWORD", "HTTPPASSWORD", "1080", "3128"} {
		if strings.Contains(string(blob), s) {
			t.Fatalf("列表泄露了 %q：%s", s, blob)
		}
	}
}

func TestEgressConnectIntegration(t *testing.T) {
	f := newFixture(t)
	c := testCipher(t)
	f.svc.Cipher = c
	f.egress(t, c, 1, "socks5://alice:SOCKSPASSWORD@203.0.113.10:1080?udp=1#s", "ENABLED", 1)
	f.egress(t, c, 2, "http://bob:HTTPPASSWORD@203.0.113.11:3128#h", "ENABLED", 3)
	f.egress(t, c, 3, "socks5://carol:x@203.0.113.12:1080#off", "DISABLED", 1)

	// 没有套餐：拒绝。
	_, err := f.svc.EgressConnect(f.ctx, f.userID, f.device, "e1")
	code(t, err, httpx.ErrNoPlan)

	f.plan(t, 1, nil, 0)
	out, err := f.svc.EgressConnect(f.ctx, f.userID, f.device, "e1")
	if err != nil {
		t.Fatal(err)
	}
	if out.Core != "xray" || out.CoreVersion != egress.XrayCoreVersion || out.Protocol != egress.SOCKS5 || !out.SupportsUDP || out.Notice != "" {
		t.Fatalf("profile %+v", out)
	}
	cfg := string(out.Config)
	for _, want := range []string{`"protocol":"tun"`, `"SOCKSPASSWORD"`, `"203.0.113.10"`, `"dns-out"`, `https://1.1.1.1/dns-query`} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("配置缺少 %s：%s", want, cfg)
		}
	}
	if strings.Contains(strings.ToLower(cfg), "allowinsecure") {
		t.Fatal("配置里出现了 allowInsecure")
	}

	// 等级不够、已停用、编号格式错误、跨设备。
	_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e2")
	code(t, err, httpx.ErrTierTooLow)
	_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e3")
	code(t, err, httpx.ErrNodeUnavailable)
	_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e99")
	code(t, err, httpx.ErrNodeUnavailable)
	for _, bad := range []string{"1", "e0", "e01", "x1", "e1 "} {
		_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, bad)
		code(t, err, httpx.ErrBadRequest)
	}
	_, err = f.svc.EgressConnect(f.ctx, f.userID, 999, "e1")
	code(t, err, httpx.ErrDeviceRevoked)

	// HTTP 出口：不支持 UDP，配置里必须有丢弃 UDP 的规则，并如实提示。
	f.plan(t, 3, nil, 0)
	out, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e2")
	if err != nil {
		t.Fatal(err)
	}
	if out.SupportsUDP || out.Notice == "" || !strings.Contains(string(out.Config), `{"network":"udp","outboundTag":"block","type":"field"}`) {
		t.Fatalf("HTTP 出口的 UDP 处理不对：%+v %s", out, out.Config)
	}

	// 密文被搬到另一行：AAD 不符，必须拒绝而不是交出别人的凭据。
	f.exec(t, `UPDATE egress_endpoints SET secret_enc=(SELECT secret_enc FROM egress_endpoints WHERE id=2) WHERE id=1`)
	_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e1")
	code(t, err, httpx.ErrNodeUnavailable)

	// 设备被移除。
	f.exec(t, `UPDATE devices SET status='REVOKED' WHERE id=$1`, f.device)
	_, err = f.svc.EgressConnect(f.ctx, f.userID, f.device, "e2")
	code(t, err, httpx.ErrDeviceRevoked)
}

func TestEgressConnectWithoutCipherFailsClosedIntegration(t *testing.T) {
	f := newFixture(t)
	f.plan(t, 1, nil, 0)
	_, err := f.svc.EgressConnect(f.ctx, f.userID, f.device, "e1")
	code(t, err, httpx.ErrInternal)
}
