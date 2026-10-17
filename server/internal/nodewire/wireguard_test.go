package nodewire

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 第 18 轮新增的 nodewire 包此前没有任何测试。本文件覆盖三件事：
// StrictJSON 的拒绝面、公开清单的校验与规范化校验和、租约的时效与时钟偏差边界。
// 全部为纯函数测试，不触碰数据库、文件系统或网络。

const wgNodeID = "9007199254740993"

// key 生成合法的 32 字节 Base64 公钥；n 不能为 0（全零公钥按规则无效）。
func key(n byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = n
	}
	return b64(b)
}

func b64(b []byte) string {
	const set = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], b[i:])
		v := uint32(chunk[0])<<16 | uint32(chunk[1])<<8 | uint32(chunk[2])
		sb.WriteByte(set[v>>18&63])
		sb.WriteByte(set[v>>12&63])
		if n > 1 {
			sb.WriteByte(set[v>>6&63])
		} else {
			sb.WriteByte('=')
		}
		if n > 2 {
			sb.WriteByte(set[v&63])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

func validManifest() WGManifest {
	return WGManifest{
		Schema:        1,
		NodeID:        wgNodeID,
		ConfigVersion: "7",
		Interface:     "wg0",
		PublicKey:     key(1),
		ListenPort:    51820,
		Peers: []WGPeer{
			{CredentialID: "2", PublicKey: key(2), AllowedIPs: []string{"10.20.0.2/32"}},
			{CredentialID: "10", PublicKey: key(3), AllowedIPs: []string{"10.20.0.10/32"}},
		},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---------------------------------------------------------------- StrictJSON

type strictInner struct {
	A int `json:"a"`
}

type strictOuter struct {
	Name     string       `json:"name"`
	Inner    strictInner  `json:"inner"`
	List     []int        `json:"list"`
	Optional string       `json:"optional,omitempty"`
	Ptr      *strictInner `json:"ptr"`
}

func strictValid() strictOuter {
	return strictOuter{Name: "n", Inner: strictInner{A: 1}, List: []int{1, 2}, Ptr: &strictInner{A: 2}}
}

func TestStrictJSONAcceptsCanonicalDocument(t *testing.T) {
	var out strictOuter
	if err := StrictJSON(mustJSON(t, strictValid()), &out); err != nil {
		t.Fatalf("合法文档被拒绝: %v", err)
	}
	if out.Name != "n" || out.Inner.A != 1 || out.Ptr == nil || out.Ptr.A != 2 {
		t.Fatal("解析结果与输入不一致")
	}
	// omitempty 字段缺失应当被接受，显式 null 指针也应当被接受。
	for _, raw := range []string{
		`{"name":"n","inner":{"a":1},"list":[1,2],"ptr":{"a":2}}`,
		`{"name":"n","inner":{"a":1},"list":[],"ptr":null}`,
	} {
		var v strictOuter
		if err := StrictJSON([]byte(raw), &v); err != nil {
			t.Fatalf("应接受 %s: %v", raw, err)
		}
	}
}

func TestStrictJSONRejects(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 12) + "1" + strings.Repeat("}", 12)
	cases := []struct {
		name string
		raw  string
	}{
		{"顶层重复键", `{"name":"a","name":"b","inner":{"a":1},"list":[],"ptr":null}`},
		{"嵌套重复键", `{"name":"a","inner":{"a":1,"a":2},"list":[],"ptr":null}`},
		{"数组内对象重复键", `{"name":"a","inner":{"a":1},"list":[],"ptr":{"a":1,"a":2}}`},
		{"未知字段", `{"name":"a","inner":{"a":1},"list":[],"ptr":null,"extra":1}`},
		{"嵌套未知字段", `{"name":"a","inner":{"a":1,"b":2},"list":[],"ptr":null}`},
		{"缺少必填字段", `{"inner":{"a":1},"list":[],"ptr":null}`},
		{"必填字段为 null", `{"name":null,"inner":{"a":1},"list":[],"ptr":null}`},
		{"尾随内容", `{"name":"a","inner":{"a":1},"list":[],"ptr":null} {}`},
		{"尾随字面量", `{"name":"a","inner":{"a":1},"list":[],"ptr":null}1`},
		{"嵌套过深", deep},
		{"截断", `{"name":"a","inner":{`},
		{"非对象", `[1,2,3]`},
		{"空输入", ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v strictOuter
			if err := StrictJSON([]byte(c.raw), &v); err == nil {
				t.Fatalf("应当拒绝但被接受: %s", c.raw)
			}
		})
	}
}

// ------------------------------------------------------------ ParseWGManifest

func TestParseWGManifestAcceptsAndCanonicalises(t *testing.T) {
	m, checksum, err := ParseWGManifest(mustJSON(t, validManifest()), wgNodeID)
	if err != nil {
		t.Fatalf("合法清单被拒绝: %v", err)
	}
	if len(checksum) != 64 {
		t.Fatalf("校验和应为 64 位十六进制，得到 %q", checksum)
	}
	// 凭据 ID 按数值而非字典序排序：10 必须排在 2 之后。
	if m.Peers[0].CredentialID != "2" || m.Peers[1].CredentialID != "10" {
		t.Fatalf("peer 未按数值排序: %v", m.Peers)
	}
}

func TestParseWGManifestChecksumIsOrderIndependent(t *testing.T) {
	a := validManifest()
	_, want, err := ParseWGManifest(mustJSON(t, a), wgNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// 交换 peer 顺序、打乱 allowed_ips 顺序，规范化后校验和必须一致。
	b := validManifest()
	b.Peers[0], b.Peers[1] = b.Peers[1], b.Peers[0]
	b.Peers[0].AllowedIPs = []string{"10.20.0.10/32"}
	_, got, err := ParseWGManifest(mustJSON(t, b), wgNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("规范化后校验和不一致: %s != %s", got, want)
	}

	// 内容真的变了，校验和必须随之改变。
	c := validManifest()
	c.Peers[0].PublicKey = key(9)
	_, changed, err := ParseWGManifest(mustJSON(t, c), wgNodeID)
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("peer 公钥变化后校验和未改变")
	}
}

func TestParseWGManifestRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WGManifest)
	}{
		{"schema 不为 1", func(m *WGManifest) { m.Schema = 2 }},
		{"节点 ID 不匹配", func(m *WGManifest) { m.NodeID = "123" }},
		{"节点 ID 为零", func(m *WGManifest) { m.NodeID = "0" }},
		{"节点 ID 前导零", func(m *WGManifest) { m.NodeID = "007" }},
		{"节点 ID 非整数", func(m *WGManifest) { m.NodeID = "abc" }},
		{"配置版本为零", func(m *WGManifest) { m.ConfigVersion = "0" }},
		{"配置版本为负", func(m *WGManifest) { m.ConfigVersion = "-1" }},
		{"接口名为 lo", func(m *WGManifest) { m.Interface = "lo" }},
		{"接口名为 all", func(m *WGManifest) { m.Interface = "all" }},
		{"接口名为 interfaces", func(m *WGManifest) { m.Interface = "interfaces" }},
		{"接口名为空", func(m *WGManifest) { m.Interface = "" }},
		{"接口名过长", func(m *WGManifest) { m.Interface = "abcdefghijklmnop" }},
		{"接口名含斜杠", func(m *WGManifest) { m.Interface = "wg/0" }},
		{"公钥全零", func(m *WGManifest) { m.PublicKey = b64(make([]byte, 32)) }},
		{"公钥长度不足", func(m *WGManifest) { m.PublicKey = b64(make([]byte, 31)) }},
		{"公钥非 Base64", func(m *WGManifest) { m.PublicKey = "!!!" }},
		{"端口为零", func(m *WGManifest) { m.ListenPort = 0 }},
		{"端口越界", func(m *WGManifest) { m.ListenPort = 65536 }},
		{"端口为负", func(m *WGManifest) { m.ListenPort = -1 }},
		{"peers 为 null", func(m *WGManifest) { m.Peers = nil }},
		{"peer 凭据 ID 重复", func(m *WGManifest) { m.Peers[1].CredentialID = m.Peers[0].CredentialID }},
		{"peer 公钥重复", func(m *WGManifest) { m.Peers[1].PublicKey = m.Peers[0].PublicKey }},
		{"peer 公钥等于节点公钥", func(m *WGManifest) { m.Peers[0].PublicKey = m.PublicKey }},
		{"peer 凭据 ID 为零", func(m *WGManifest) { m.Peers[0].CredentialID = "0" }},
		{"peer IP 重复", func(m *WGManifest) { m.Peers[1].AllowedIPs = m.Peers[0].AllowedIPs }},
		{"peer 无 IP", func(m *WGManifest) { m.Peers[0].AllowedIPs = []string{} }},
		{"peer IP 超过两个", func(m *WGManifest) {
			m.Peers[0].AllowedIPs = []string{"10.20.0.2/32", "10.20.0.3/32", "10.20.0.4/32"}
		}},
		{"peer IP 为公网地址", func(m *WGManifest) { m.Peers[0].AllowedIPs = []string{"8.8.8.8/32"} }},
		{"peer IP 非主机前缀", func(m *WGManifest) { m.Peers[0].AllowedIPs = []string{"10.20.0.0/24"} }},
		{"peer IP 非规范写法", func(m *WGManifest) { m.Peers[0].AllowedIPs = []string{"010.20.0.2/32"} }},
		{"peer IP 为 4in6", func(m *WGManifest) { m.Peers[0].AllowedIPs = []string{"::ffff:10.20.0.2/128"} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validManifest()
			c.mutate(&m)
			if _, _, err := ParseWGManifest(mustJSON(t, m), wgNodeID); err == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestParseWGManifestRejectsOversizeAndTooManyPeers(t *testing.T) {
	m := validManifest()
	m.Peers = nil
	for i := 1; i <= 1001; i++ {
		m.Peers = append(m.Peers, WGPeer{
			CredentialID: fmt.Sprint(i),
			PublicKey:    key(byte(i%254 + 1)),
			AllowedIPs:   []string{fmt.Sprintf("10.%d.%d.%d/32", i/65536%256, i/256%256, i%256)},
		})
	}
	if _, _, err := ParseWGManifest(mustJSON(t, m), wgNodeID); err == nil {
		t.Fatal("超过 1000 个 peer 应当被拒绝")
	}

	oversize := append(mustJSON(t, validManifest()), make([]byte, 1048577)...)
	if _, _, err := ParseWGManifest(oversize, wgNodeID); err == nil {
		t.Fatal("超过 1 MiB 的清单应当被拒绝")
	}
}

// --------------------------------------------------------------- ParseWGLease

const wgNonceOK = "0123456789abcdef0123456789abcdef"

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func leaseFor(t *testing.T, issued time.Time, ttl time.Duration) ([]byte, time.Time) {
	t.Helper()
	m := validManifest()
	_, checksum, err := ParseWGManifest(mustJSON(t, m), wgNodeID)
	if err != nil {
		t.Fatal(err)
	}
	l := WGLease{
		Schema:       1,
		RequestNonce: wgNonceOK,
		IssuedAt:     stamp(issued),
		ExpiresAt:    stamp(issued.Add(ttl)),
		Checksum:     checksum,
		Manifest:     m,
	}
	return mustJSON(t, l), issued
}

func TestParseWGLeaseAcceptsInsideWindow(t *testing.T) {
	issued := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	raw, _ := leaseFor(t, issued, 30*time.Second)
	for _, now := range []time.Time{issued, issued.Add(15 * time.Second), issued.Add(29 * time.Second)} {
		if _, err := ParseWGLease(raw, wgNodeID, now); err != nil {
			t.Fatalf("窗口内 %s 被拒绝: %v", now, err)
		}
	}
}

func TestParseWGLeaseRejectsOutsideWindow(t *testing.T) {
	issued := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	raw, _ := leaseFor(t, issued, 30*time.Second)
	cases := []struct {
		name string
		now  time.Time
	}{
		{"恰好到期", issued.Add(30 * time.Second)},
		{"已过期", issued.Add(31 * time.Second)},
		{"远期过期", issued.Add(time.Hour)},
		{"时钟落后于签发时间", issued.Add(-time.Second)},
		{"时钟严重回拨", issued.Add(-time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseWGLease(raw, wgNodeID, c.now); err == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestParseWGLeaseRejectsOverlongTTL(t *testing.T) {
	issued := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	// 31 秒超过 30 秒上限；签发方不得自行延长租约。
	raw, _ := leaseFor(t, issued, 31*time.Second)
	if _, err := ParseWGLease(raw, wgNodeID, issued.Add(time.Second)); err == nil {
		t.Fatal("超过 30 秒的租约应当被拒绝")
	}
	// 到期时间不晚于签发时间。
	for _, ttl := range []time.Duration{0, -time.Second} {
		raw, _ := leaseFor(t, issued, ttl)
		if _, err := ParseWGLease(raw, wgNodeID, issued); err == nil {
			t.Fatalf("TTL=%s 应当被拒绝", ttl)
		}
	}
}

func TestParseWGLeaseRejectsMalformedFields(t *testing.T) {
	issued := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	now := issued.Add(time.Second)
	base := func(t *testing.T) WGLease {
		t.Helper()
		raw, _ := leaseFor(t, issued, 30*time.Second)
		var l WGLease
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	cases := []struct {
		name   string
		mutate func(*WGLease)
	}{
		{"schema 不为 1", func(l *WGLease) { l.Schema = 2 }},
		{"nonce 过短", func(l *WGLease) { l.RequestNonce = "abc" }},
		{"nonce 含大写", func(l *WGLease) { l.RequestNonce = strings.ToUpper(wgNonceOK) }},
		{"nonce 非十六进制", func(l *WGLease) { l.RequestNonce = strings.Repeat("z", 32) }},
		{"签发时间带时区偏移", func(l *WGLease) { l.IssuedAt = "2026-09-13T10:00:00+08:00" }},
		{"签发时间无 Z", func(l *WGLease) { l.IssuedAt = "2026-09-13T10:00:00" }},
		{"到期时间格式错误", func(l *WGLease) { l.ExpiresAt = "not-a-time" }},
		{"校验和不匹配", func(l *WGLease) { l.Checksum = strings.Repeat("0", 64) }},
		{"校验和为空", func(l *WGLease) { l.Checksum = "" }},
		{"清单被篡改而校验和不变", func(l *WGLease) { l.Manifest.Peers[0].PublicKey = key(9) }},
		{"清单节点 ID 不匹配", func(l *WGLease) { l.Manifest.NodeID = "123" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := base(t)
			c.mutate(&l)
			if _, err := ParseWGLease(mustJSON(t, l), wgNodeID, now); err == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestParseWGLeaseRejectsRawManifestAsAuthorization(t *testing.T) {
	// 裸清单没有时效信息，绝不能被当作当前授权。
	if _, err := ParseWGLease(mustJSON(t, validManifest()), wgNodeID, time.Now()); err == nil {
		t.Fatal("裸清单被当作有效租约接受")
	}
}

func TestValidWGNonceAndInterface(t *testing.T) {
	if !ValidWGNonce(wgNonceOK) {
		t.Fatal("合法 nonce 被拒绝")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.ToUpper(wgNonceOK)} {
		if ValidWGNonce(bad) {
			t.Fatalf("非法 nonce 被接受: %q", bad)
		}
	}
	if !ValidWGInterface("wg0") {
		t.Fatal("wg0 应为合法接口名")
	}
	for _, bad := range []string{"lo", "all", "interfaces", "", "wg 0", strings.Repeat("w", 16)} {
		if ValidWGInterface(bad) {
			t.Fatalf("非法接口名被接受: %q", bad)
		}
	}
}
