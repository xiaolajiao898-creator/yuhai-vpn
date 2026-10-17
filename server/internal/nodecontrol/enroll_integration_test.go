package nodecontrol

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 注册是整个系统里唯一一处签发身份的地方，所以这里的测试重点全在「不该发的绝不发」：
// 令牌只能用一次、CSR 里的身份主张一律不采信、签出来的东西只能当客户端证书用。

type enrollFixture struct {
	*heartbeatFixture
	token string
}

func newEnrollFixture(t *testing.T) *enrollFixture {
	t.Helper()
	f := newHeartbeatFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"admin_users", "node_enroll_tokens", "node_enrollments", "node_inbounds"} {
		f.exec(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}
	for _, sql := range []string{
		"GRANT SELECT,INSERT,UPDATE ON node_enroll_tokens TO " + role,
		"GRANT SELECT,INSERT ON node_enrollments TO " + role,
		"GRANT SELECT ON node_inbounds TO " + role,
		"GRANT UPDATE(agent_cert_fingerprint,agent_cert_prev_fingerprint,agent_cert_prev_until,agent_version,updated_at) ON nodes TO " + role,
		"GRANT USAGE,SELECT ON SEQUENCE " + pgx.Identifier{"node_enrollments_id_seq"}.Sanitize() + " TO " + role,
	} {
		f.exec(t, sql)
	}
	f.exec(t, `INSERT INTO admin_users(id,username,password_hash,totp_secret_enc,status)
 OVERRIDING SYSTEM VALUE VALUES(1,'fixture','x','y','ACTIVE')`)
	f.exec(t, `UPDATE nodes SET status='MAINTENANCE',agent_cert_fingerprint=NULL`)
	f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params,priority,status)
 VALUES(9007199254740993,'WIREGUARD',51820,$1::jsonb,1,'ENABLED')`,
		`{"interface":"wg0","public_key":"`+enrollWGKey+`","config_version":"1"}`)

	// 用测试 CA 当签发 CA：它已经在 roots 里，签出来的证书本控制面自己就认。
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certPath, f.ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(f.ca.key)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	ca, err := LoadEnrollCA(certPath, keyPath, f.ca.roots, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.svc.EnrollCA = ca

	e := &enrollFixture{heartbeatFixture: f}
	e.token = e.issueToken(t, f.clock.Now().Add(24*time.Hour))
	return e
}

func (f *enrollFixture) issueToken(t *testing.T, expires time.Time) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	f.exec(t, `INSERT INTO node_enroll_tokens(node_id,token_hash,expires_at,created_by,created_at)
 VALUES(9007199254740993,$1,$2,1,$3)`, tokenDigest(token), expires, f.clock.Now())
	return token
}

// csrPEM 造一个真实的证书请求。claimURI 用来验证「CSR 里的身份主张被无视」。
func csrPEM(t *testing.T, claimURI string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "whatever-the-node-claims"}}
	if claimURI != "" {
		u, e := url.Parse(claimURI)
		if e != nil {
			t.Fatal(e)
		}
		tmpl.URIs = []*url.URL{u}
		tmpl.DNSNames = []string{"evil.example.net"}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

const enrollWGKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
const enrollRealityKey = "IiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/QEE="

func enrollBody(token, csr string) []byte {
	return []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q,"reality_public_key":%q,"wg_public_key":%q,"agent_version":"1.2.3"}`,
		token, csr, enrollRealityKey, enrollWGKey))
}

func TestEnrollIssuesClientCertificateIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	// CSR 里声称自己是别的节点、还带了 DNS 名——这些主张必须被完全无视。
	out, err := f.svc.Enroll(f.ctx, enrollBody(f.token, csrPEM(t, "spiffe://yuhai.internal/node/424242")))
	if err != nil {
		t.Fatal(err)
	}
	if out.NodeID != "9007199254740993" {
		t.Fatalf("节点编号不符：%s", out.NodeID)
	}
	block, rest := pem.Decode([]byte(out.CertPEM))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatal("返回的不是单个 PEM 证书")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// 身份只能由令牌决定。
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://yuhai.internal/node/9007199254740993" {
		t.Fatalf("签出的身份被 CSR 左右了：%v", leaf.URIs)
	}
	if len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 {
		t.Fatalf("CSR 里的其它名称被带进了证书：%v %v", leaf.DNSNames, leaf.IPAddresses)
	}
	// 只能当客户端证书用。
	if leaf.IsCA || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("证书用途不符：isCA=%v eku=%v", leaf.IsCA, leaf.ExtKeyUsage)
	}
	if leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Fatal("签出的证书带有签发能力")
	}
	// 序列号必须是随机的大整数，不能是可预测的小数字。
	if leaf.SerialNumber.BitLen() < 64 {
		t.Fatalf("序列号太短：%s", leaf.SerialNumber)
	}
	// 本控制面自己的身份校验必须认它——否则等于发了一张废证书。
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: f.ca.roots, CurrentTime: f.clock.Now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("签出的证书本控制面不认：%v", err)
	}

	// 指纹绑定与注册记录必须同时落库。
	sum := sha256.Sum256(block.Bytes)
	want := hex.EncodeToString(sum[:])
	var pin, version string
	if err = f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint,agent_version FROM nodes WHERE id=9007199254740993`).
		Scan(&pin, &version); err != nil {
		t.Fatal(err)
	}
	if pin != want || version != "1.2.3" {
		t.Fatalf("指纹或版本未绑定：%s %s", pin, version)
	}
	var wg, reality string
	var notAfter time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT wg_public_key,reality_public_key,cert_not_after
 FROM node_enrollments WHERE node_id=9007199254740993`).Scan(&wg, &reality, &notAfter); err != nil {
		t.Fatal(err)
	}
	if wg != enrollWGKey || reality != enrollRealityKey {
		t.Fatalf("节点上报的公钥未记录：%s %s", wg, reality)
	}
	// 证书不得超出 CA 自身有效期。
	if notAfter.After(f.ca.cert.NotAfter) || !notAfter.Equal(leaf.NotAfter) {
		t.Fatalf("有效期不符：%v / CA %v", notAfter, f.ca.cert.NotAfter)
	}
	// 令牌已被标记使用。
	var used *time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT used_at FROM node_enroll_tokens WHERE token_hash=$1`,
		tokenDigest(f.token)).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used == nil {
		t.Fatal("令牌未被标记使用")
	}
	// 响应里不得出现 CA 私钥之类的东西；CA 链只应含公开证书。
	blob, _ := json.Marshal(out)
	for _, secret := range []string{"PRIVATE KEY", "enroll_token"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("响应里出现了 %q", secret)
		}
	}
}

func TestEnrollTokenIsSingleUseIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	if _, err := f.svc.Enroll(f.ctx, enrollBody(f.token, csrPEM(t, ""))); err != nil {
		t.Fatal(err)
	}
	// 同一把令牌再用一次：必须拒绝，且不得改动已绑定的指纹。
	var before string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint FROM nodes WHERE id=9007199254740993`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Enroll(f.ctx, enrollBody(f.token, csrPEM(t, "")))
	expectCode(t, err, httpx.ErrForbidden)
	var after string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint FROM nodes WHERE id=9007199254740993`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("被拒的重复注册改动了证书绑定")
	}
	if n := enrollCount(t, f); n != 1 {
		t.Fatalf("注册记录应当只有 1 条，实际 %d", n)
	}
}

func enrollCount(t *testing.T, f *enrollFixture) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM node_enrollments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnrollConcurrentSameTokenSucceedsOnceIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	const attempts = 6
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.svc.Enroll(f.ctx, enrollBody(f.token, csrPEM(t, "")))
		}(i)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发注册成功了 %d 次，应当只有 1 次", success)
	}
	if n := enrollCount(t, f); n != 1 {
		t.Fatalf("并发注册留下了 %d 条记录", n)
	}
}

func TestEnrollRejectsBadTokensAndStatesIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	csr := csrPEM(t, "")

	// 未知令牌。
	unknown := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	expectCodeOnly(t, f, enrollBody(unknown, csr), httpx.ErrForbidden)

	// 已过期的令牌。
	expired := f.issueToken(t, f.clock.Now().Add(-time.Second))
	expectCodeOnly(t, f, enrollBody(expired, csr), httpx.ErrForbidden)

	// 节点已被停用：吊销之后不得用旧令牌注册回来。
	f.exec(t, `UPDATE nodes SET status='DISABLED'`)
	expectCodeOnly(t, f, enrollBody(f.token, csr), httpx.ErrForbidden)
	f.exec(t, `UPDATE nodes SET status='MAINTENANCE'`)

	if n := enrollCount(t, f); n != 0 {
		t.Fatalf("被拒的注册留下了 %d 条记录", n)
	}
}

func expectCodeOnly(t *testing.T, f *enrollFixture, body []byte, code httpx.Code) {
	t.Helper()
	_, err := f.svc.Enroll(f.ctx, body)
	expectCode(t, err, code)
}

func TestEnrollRejectsBadRequestsIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	good := csrPEM(t, "")

	weakRSA, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weakDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, weakRSA)
	if err != nil {
		t.Fatal(err)
	}
	weakPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: weakDER}))

	// 签名被破坏的 CSR：解析得动，但自签名验不过。
	block, _ := pem.Decode([]byte(good))
	tampered := append([]byte(nil), block.Bytes...)
	tampered[len(tampered)-1] ^= 0xFF
	tamperedPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered}))

	cases := map[string][]byte{
		"令牌格式不对":     enrollBody("short", good),
		"CSR 不是 PEM": enrollBody(f.token, "not a pem"),
		"PEM 类型不对":   enrollBody(f.token, strings.ReplaceAll(good, "CERTIFICATE REQUEST", "CERTIFICATE")),
		"CSR 签名被篡改":  enrollBody(f.token, tamperedPEM),
		"RSA 位数不足":   enrollBody(f.token, weakPEM),
		"WG 公钥无效": []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q,"reality_public_key":%q,"wg_public_key":"nope","agent_version":"1.0.0"}`,
			f.token, good, enrollRealityKey)),
		"REALITY 公钥无效": []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q,"reality_public_key":"nope","wg_public_key":%q,"agent_version":"1.0.0"}`,
			f.token, good, enrollWGKey)),
		"版本号含非法字符": []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q,"reality_public_key":%q,"wg_public_key":%q,"agent_version":"1.0.0 rm -rf"}`,
			f.token, good, enrollRealityKey, enrollWGKey)),
		"多余字段": []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q,"reality_public_key":%q,"wg_public_key":%q,"agent_version":"1.0.0","extra":1}`,
			f.token, good, enrollRealityKey, enrollWGKey)),
		"缺字段":  []byte(fmt.Sprintf(`{"enroll_token":%q,"csr_pem":%q}`, f.token, good)),
		"不是对象": []byte(`[]`),
		"空请求":  []byte(``),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Enroll(f.ctx, body)
			expectCode(t, err, httpx.ErrBadRequest)
		})
	}
	// 一条都不能落库，令牌也不能被消耗掉。
	if n := enrollCount(t, f); n != 0 {
		t.Fatalf("非法请求留下了 %d 条注册记录", n)
	}
	var used *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT used_at FROM node_enroll_tokens WHERE token_hash=$1`,
		tokenDigest(f.token)).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != nil {
		t.Fatal("非法请求消耗了令牌")
	}
}

func TestEnrollAcceptsEd25519AndDisabledByDefaultIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, priv)
	if err != nil {
		t.Fatal(err)
	}
	csr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	if _, err = f.svc.Enroll(f.ctx, enrollBody(f.token, csr)); err != nil {
		t.Fatalf("Ed25519 CSR 应当被接受：%v", err)
	}

	// 未配置签发 CA 时，注册一律 404——路由本身也不会注册。
	f.svc.EnrollCA = nil
	_, err = f.svc.Enroll(f.ctx, enrollBody(f.issueToken(t, f.clock.Now().Add(time.Hour)), csrPEM(t, "")))
	expectCode(t, err, httpx.ErrNotFound)
}

func TestLoadEnrollCARejectsUnusableInputsIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(f.ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := write("ca.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	certPath := write("ca.crt", f.ca.pem)

	// 私钥与证书不匹配。
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, _ := x509.MarshalPKCS8PrivateKey(other)
	mismatch := write("other.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: otherDER}))
	if _, err = LoadEnrollCA(certPath, mismatch, f.ca.roots, f.clock.Now()); err == nil {
		t.Fatal("私钥与证书不匹配却通过了")
	}
	// CA 不在客户端信任根里：签出来的证书没人认，必须拒绝启动。
	stranger := newCA(t, f.clock)
	strangerCert := write("stranger.crt", stranger.pem)
	strangerKeyDER, _ := x509.MarshalPKCS8PrivateKey(stranger.key)
	strangerKey := write("stranger.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: strangerKeyDER}))
	if _, err = LoadEnrollCA(strangerCert, strangerKey, f.ca.roots, f.clock.Now()); err == nil {
		t.Fatal("不在信任根里的 CA 却通过了")
	}
	// 不是 CA 的证书。
	leaf := f.ca.issue(t, "spiffe://yuhai.internal/node/1", nil)
	leafPath := write("leaf.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}))
	if _, err = LoadEnrollCA(leafPath, keyPath, f.ca.roots, f.clock.Now()); err == nil {
		t.Fatal("非 CA 证书却通过了")
	}
	// 文件不存在、格式不对。
	if _, err = LoadEnrollCA(filepath.Join(dir, "missing"), keyPath, f.ca.roots, f.clock.Now()); err == nil {
		t.Fatal("缺文件却通过了")
	}
	if _, err = LoadEnrollCA(certPath, write("junk.key", []byte("not pem")), f.ca.roots, f.clock.Now()); err == nil {
		t.Fatal("非 PEM 私钥却通过了")
	}
	// CA 已过期。
	if _, err = LoadEnrollCA(certPath, keyPath, f.ca.roots, f.ca.cert.NotAfter.Add(time.Second)); err == nil {
		t.Fatal("过期 CA 却通过了")
	}
}

// ---------- 证书续期 ----------

// renewState 造一个「已经用了 n 分之几寿命」的客户端证书连接状态。
func (f *enrollFixture) enrolledState(t *testing.T, elapsed float64) (*tls.ConnectionState, string) {
	t.Helper()
	out, err := f.svc.Enroll(f.ctx, enrollBody(f.token, csrPEM(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(out.CertPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(block.Bytes)
	// 把时钟推到证书寿命的指定比例处。
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	f.clock.set(leaf.NotBefore.Add(time.Duration(float64(life) * elapsed)))
	state := &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13,
		PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	return state, hex.EncodeToString(sum[:])
}

func renewBody(csr string) []byte {
	return []byte(fmt.Sprintf(`{"csr_pem":%q}`, csr))
}

func TestRenewRebindsWithOverlapIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, oldPin := f.enrolledState(t, 0.9)

	out, err := f.svc.Renew(f.ctx, state, renewBody(csrPEM(t, "")))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(out.CertPEM))
	if block == nil {
		t.Fatal("续期未返回证书")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	// 身份必须原样保留——续期不是换身份。
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://yuhai.internal/node/9007199254740993" {
		t.Fatalf("续期改变了身份：%v", leaf.URIs)
	}
	sum := sha256.Sum256(block.Bytes)
	newPin := hex.EncodeToString(sum[:])

	var cur, prev string
	var until time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint,agent_cert_prev_fingerprint,agent_cert_prev_until
 FROM nodes WHERE id=9007199254740993`).Scan(&cur, &prev, &until); err != nil {
		t.Fatal(err)
	}
	if cur != newPin || prev != oldPin {
		t.Fatalf("换绑不符：cur=%s prev=%s", cur, prev)
	}
	// 重叠窗口内旧证书仍被接受；窗口过后立刻失效。
	cert := nodeCert{Current: &cur, Prev: &prev, Until: &until}
	if !cert.accepts(oldPin, f.clock.Now()) {
		t.Fatal("重叠窗口内旧指纹就被拒了——响应一丢节点就永久失联")
	}
	if cert.accepts(oldPin, until.Add(time.Second)) {
		t.Fatal("重叠窗口过后旧指纹仍被接受")
	}
	if !cert.accepts(newPin, until.Add(time.Hour)) {
		t.Fatal("新指纹不被接受")
	}
	// 续期也要留一条签发记录，且不带注册令牌。
	var kind string
	var token *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT kind,enroll_token_id FROM node_enrollments
 WHERE cert_fingerprint=$1`, newPin).Scan(&kind, &token); err != nil {
		t.Fatal(err)
	}
	if kind != "RENEW" || token != nil {
		t.Fatalf("续期记录不符：%s %v", kind, token)
	}
}

func TestRenewRefusesTooEarlyIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, oldPin := f.enrolledState(t, 0.1)
	_, err := f.svc.Renew(f.ctx, state, renewBody(csrPEM(t, "")))
	expectCode(t, err, httpx.ErrConflict)
	// 被拒时绝不能动换绑。
	var cur string
	var prev *string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint,agent_cert_prev_fingerprint
 FROM nodes WHERE id=9007199254740993`).Scan(&cur, &prev); err != nil {
		t.Fatal(err)
	}
	if cur != oldPin || prev != nil {
		t.Fatalf("被拒的续期改动了绑定：%s %v", cur, prev)
	}
}

func TestRenewFromPreviousKeepsWindowIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, oldPin := f.enrolledState(t, 0.9)
	// 第一次续期成功，但设想响应丢了：节点手里还是旧证书。
	if _, err := f.svc.Renew(f.ctx, state, renewBody(csrPEM(t, ""))); err != nil {
		t.Fatal(err)
	}
	var firstUntil time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_prev_until FROM nodes WHERE id=9007199254740993`).
		Scan(&firstUntil); err != nil {
		t.Fatal(err)
	}
	// 节点拿着旧证书重试：必须仍然能续，且 prev 不能被挤掉。
	if _, err := f.svc.Renew(f.ctx, state, renewBody(csrPEM(t, ""))); err != nil {
		t.Fatalf("窗口内用旧证书重试应当成功：%v", err)
	}
	var prev string
	var until time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_prev_fingerprint,agent_cert_prev_until
 FROM nodes WHERE id=9007199254740993`).Scan(&prev, &until); err != nil {
		t.Fatal(err)
	}
	if prev != oldPin {
		t.Fatalf("重试把节点手里那张证书挤掉了：prev=%s", prev)
	}
	if !until.Equal(firstUntil) {
		t.Fatal("重试无限延长了重叠窗口")
	}
}

func TestRenewRefusesRevokedAndBadInputIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, _ := f.enrolledState(t, 0.9)

	// 非法 CSR。
	_, err := f.svc.Renew(f.ctx, state, renewBody("not a pem"))
	expectCode(t, err, httpx.ErrBadRequest)
	_, err = f.svc.Renew(f.ctx, state, []byte(`{"csr_pem":"x","extra":1}`))
	expectCode(t, err, httpx.ErrBadRequest)

	// 没有客户端证书：续期必须 mTLS。
	_, err = f.svc.Renew(f.ctx, &tls.ConnectionState{}, renewBody(csrPEM(t, "")))
	expectCode(t, err, httpx.ErrForbidden)

	// 节点被吊销（指纹清空）后不得续期——否则吊销等于没吊销。
	f.exec(t, `UPDATE nodes SET agent_cert_fingerprint=NULL,agent_cert_prev_fingerprint=NULL,
 agent_cert_prev_until=NULL WHERE id=9007199254740993`)
	_, err = f.svc.Renew(f.ctx, state, renewBody(csrPEM(t, "")))
	expectCode(t, err, httpx.ErrForbidden)
}

// ---------- 节点自取配置 ----------

func TestNodeConfigReturnsEnabledInboundsOnlyIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, _ := f.enrolledState(t, 0.1)
	f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params,priority,status)
 VALUES(9007199254740993,'WIREGUARD',51821,$1::jsonb,2,'DISABLED')`,
		`{"interface":"wg1","public_key":"`+enrollWGKey+`","config_version":"2"}`)

	out, err := f.svc.Config(f.ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	// 夹具里已有一条 ENABLED 的 51820；刚加的 51821 是 DISABLED，不得下发。
	if len(out.Inbounds) != 1 || out.Inbounds[0].Port != 51820 {
		t.Fatalf("只应下发 ENABLED 入站：%+v", out.Inbounds)
	}
	if out.Agent.HeartbeatSeconds != 30 || out.Agent.EventPollSeconds != 10 {
		t.Fatalf("agent 参数不符：%+v", out.Agent)
	}
	if out.ConfigVersion <= 0 {
		t.Fatalf("配置版本必须是正整数：%d", out.ConfigVersion)
	}
	// 版本是内容指纹：内容不变则不变，内容一变必定变。
	again, err := f.svc.Config(f.ctx, state)
	if err != nil || again.ConfigVersion != out.ConfigVersion {
		t.Fatalf("内容未变但版本变了：%d → %d (%v)", out.ConfigVersion, again.ConfigVersion, err)
	}
	f.exec(t, `UPDATE node_inbounds SET status='ENABLED' WHERE port=51821`)
	changed, err := f.svc.Config(f.ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ConfigVersion == out.ConfigVersion || len(changed.Inbounds) != 2 {
		t.Fatalf("内容变了但版本没变：%d", changed.ConfigVersion)
	}
	// 配置里不得出现任何用户凭证或密钥。
	blob, _ := json.Marshal(changed)
	for _, secret := range []string{"proxy_secret", "proxy_uuid", "private_key", "PRIVATE"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("配置里出现了 %q", secret)
		}
	}
}

func TestNodeConfigRefusesRevokedAndAnonymousIntegration(t *testing.T) {
	f := newEnrollFixture(t)
	state, _ := f.enrolledState(t, 0.1)
	// 没有客户端证书。
	_, err := f.svc.Config(f.ctx, &tls.ConnectionState{})
	expectCode(t, err, httpx.ErrForbidden)

	// 节点被停用。
	f.exec(t, `UPDATE nodes SET status='DISABLED'`)
	_, err = f.svc.Config(f.ctx, state)
	expectCode(t, err, httpx.ErrForbidden)

	// 证书绑定被解除。
	f.exec(t, `UPDATE nodes SET status='ONLINE',agent_cert_fingerprint=NULL,
 agent_cert_prev_fingerprint=NULL,agent_cert_prev_until=NULL`)
	_, err = f.svc.Config(f.ctx, state)
	expectCode(t, err, httpx.ErrForbidden)
}
