package nodecontrol

import (
	"bytes"
	"context"
	"crypto"
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
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 节点注册：用一次性令牌把一台真实机器接进控制面（方案 node.yaml 的 POST /node/v1/enroll）。
//
// 这是整个系统里唯一一处**签发身份**的代码，所以边界写得比别处更死：
//
//  1. **CSR 里除了公钥，其余一概不信。** 主体、SAN、扩展全部丢弃，证书模板由控制面自己
//     构造：URI SAN 恒为 spiffe://yuhai.internal/node/<令牌所属节点 id>。节点无法通过
//     构造 CSR 给自己换一个身份——它拿到的 id 只由令牌决定。
//  2. **只签客户端证书。** ExtKeyUsage 仅 ClientAuth，IsCA=false，路径长度受限。
//     签出来的东西不能当服务端证书用，更不能再去签别的证书。
//  3. **令牌一次性且与节点绑定。** 行锁 → 校验未用未过期 → 标记已用 → 绑定指纹，
//     全在同一事务里。并发用同一把令牌只有一个能成功。
//  4. **不做响应重放。** 证书只在签发的那一次返回；令牌已用即失效。丢了就让后台重签一把，
//     这比把已签发证书留在库里可回放要安全。
//  5. **签发成功才绑定指纹。** nodes.agent_cert_fingerprint 与证书在同一事务里写入，
//     不会出现「证书发出去了但控制面不认」或反过来的中间态。
type EnrollInput struct {
	EnrollToken      string `json:"enroll_token"`
	CSRPEM           string `json:"csr_pem"`
	RealityPublicKey string `json:"reality_public_key"`
	WGPublicKey      string `json:"wg_public_key"`
	AgentVersion     string `json:"agent_version"`
}

type EnrollResult struct {
	NodeID        string `json:"node_id"`
	CertPEM       string `json:"cert_pem"`
	CAChainPEM    string `json:"ca_chain_pem"`
	CertExpiresAt string `json:"cert_expires_at"`
}

// EnrollCA 是签发节点客户端证书的 CA。只在 NODE_ENROLL_ENABLED=true 时加载。
type EnrollCA struct {
	Cert    *x509.Certificate
	Key     crypto.Signer
	ChainPE string // CA 证书链 PEM，随签发结果一起返回给节点
}

// 证书有效期。比令牌长得多，但远短于 CA：过期后节点用 /cert/renew 续期（尚未实现），
// 或由后台重新签发注册令牌。
const enrollCertTTL = 90 * 24 * time.Hour

// 允许的时钟偏差：证书生效时间往前挪一点，避免节点时钟稍快就用不了刚拿到的证书。
const enrollClockSkew = 5 * time.Minute

func badEnroll() error { return httpx.NewError(httpx.ErrBadRequest, "注册请求无效") }

// LoadEnrollCA 在启动时加载并校验签发 CA。任何一项不对就拒绝启动——
// 把一个用不了的 CA 留到运行时才发现，意味着节点注册会在最需要它的时候失败。
func LoadEnrollCA(certFile, keyFile string, roots *x509.CertPool, now time.Time) (*EnrollCA, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil || len(certPEM) == 0 || len(certPEM) > 1024*1024 {
		return nil, errors.New("注册 CA 证书文件无效")
	}
	block, rest := pem.Decode(bytes.TrimSpace(certPEM))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("注册 CA 证书必须是单个 PEM 证书")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("注册 CA 证书不具备签发能力")
	}
	if now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return nil, errors.New("注册 CA 证书不在有效期内")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil || len(keyPEM) == 0 || len(keyPEM) > 1024*1024 {
		return nil, errors.New("注册 CA 私钥文件无效")
	}
	kb, krest := pem.Decode(bytes.TrimSpace(keyPEM))
	if kb == nil || len(bytes.TrimSpace(krest)) != 0 {
		return nil, errors.New("注册 CA 私钥必须是单个 PEM 块")
	}
	if len(kb.Headers) != 0 {
		// 带头部通常意味着是加密私钥；这里不接受，交由部署方用 KMS 或文件权限保护。
		return nil, errors.New("注册 CA 私钥不能带 PEM 头部")
	}
	key, err := parsePrivateKey(kb)
	if err != nil {
		return nil, err
	}
	// 私钥必须确实对应这张证书，否则签出来的证书谁也验不过。
	if !ca.PublicKey.(interface{ Equal(crypto.PublicKey) bool }).Equal(key.Public()) {
		return nil, errors.New("注册 CA 私钥与证书不匹配")
	}
	// CA 必须在客户端信任的根里，否则签出来的证书连自己的控制面都不认。
	if roots == nil {
		return nil, errors.New("缺少节点客户端 CA")
	}
	if !rootsContain(roots, ca) {
		return nil, errors.New("注册 CA 不在节点客户端 CA 列表中，签出的证书不会被接受")
	}
	return &EnrollCA{Cert: ca, Key: key,
		ChainPE: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}, nil
}

// rootsContain 用 Subjects 的原始 DER 比对——x509.CertPool 不暴露证书本身，
// 但自签 CA 的 subject 唯一足以判断它是否在池内；再用一次实际验证兜底。
func rootsContain(roots *x509.CertPool, ca *x509.Certificate) bool {
	if _, err := ca.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: ca.NotBefore.Add(time.Second),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err == nil {
		return true
	}
	return false
}

func parsePrivateKey(b *pem.Block) (crypto.Signer, error) {
	invalid := errors.New("注册 CA 私钥格式不支持")
	var key any
	var err error
	switch b.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(b.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(b.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(b.Bytes)
	default:
		return nil, invalid
	}
	if err != nil {
		return nil, invalid
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, invalid
	}
	return signer, nil
}

// parseCSR 解析并校验证书请求。返回的只有公钥——CSR 里其余内容一律不采用。
func parseCSR(text string) (crypto.PublicKey, error) {
	if len(text) == 0 || len(text) > 16*1024 {
		return nil, badEnroll()
	}
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(block.Headers) != 0 ||
		len(bytes.TrimSpace(rest)) != 0 {
		return nil, badEnroll()
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || !bytes.Equal(csr.Raw, block.Bytes) {
		return nil, badEnroll()
	}
	// 自签名必须校验通过：证明提交者确实持有对应私钥。
	if err = csr.CheckSignature(); err != nil {
		return nil, badEnroll()
	}
	switch k := csr.PublicKey.(type) {
	case ed25519.PublicKey:
		if len(k) != ed25519.PublicKeySize {
			return nil, badEnroll()
		}
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() && k.Curve != elliptic.P384() {
			return nil, badEnroll()
		}
		if !k.Curve.IsOnCurve(k.X, k.Y) {
			return nil, badEnroll()
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 || k.N.BitLen() > 4096 || k.E < 3 {
			return nil, badEnroll()
		}
	default:
		return nil, badEnroll()
	}
	return csr.PublicKey, nil
}

func validAgentVersion(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			r != '.' && r != '-' && r != '+' && r != '_' {
			return false
		}
	}
	return true
}

// validBase64Key32 用于 reality_public_key：格式必须是 32 字节 Base64。
// 本轮不支持 REALITY 入站，所以只登记节点报上来的值，不用它生成任何配置。
func validBase64Key32(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != s {
		return false
	}
	var v byte
	for _, c := range b {
		v |= c
	}
	return v != 0
}

func decodeEnroll(raw []byte) (EnrollInput, error) {
	var in EnrollInput
	if len(raw) == 0 || len(raw) > 32*1024 || nodewire.StrictJSON(raw, &in) != nil {
		return in, badEnroll()
	}
	if !validToken64(in.EnrollToken) || !validAgentVersion(in.AgentVersion) ||
		!nodewire.ValidWGPublicKey(in.WGPublicKey) || !validBase64Key32(in.RealityPublicKey) {
		return in, badEnroll()
	}
	return in, nil
}

// validToken64 与后台签发格式一致：32 字节随机、Base64URL 无填充，共 43 个字符。
func validToken64(s string) bool {
	if len(s) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

func tokenDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Enroll 用一次性令牌换取节点客户端证书。
func (s *Service) Enroll(ctx context.Context, raw []byte) (EnrollResult, error) {
	var out EnrollResult
	if s.EnrollCA == nil {
		return out, httpx.NewError(httpx.ErrNotFound, "节点注册未启用")
	}
	in, err := decodeEnroll(raw)
	if err != nil {
		return out, err
	}
	pub, err := parseCSR(in.CSRPEM)
	if err != nil {
		return out, err
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(stop)
	}()

	now := s.Clock.Now()
	// 行锁：同一把令牌被并发使用时，只有一个事务能走到标记已用。
	var tokenID, nodeID int64
	var used *time.Time
	var expires time.Time
	err = tx.QueryRow(c, `SELECT id,node_id,used_at,expires_at FROM node_enroll_tokens
 WHERE token_hash=$1 FOR UPDATE`, tokenDigest(in.EnrollToken)).Scan(&tokenID, &nodeID, &used, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrForbidden, "注册令牌无效")
	}
	if err != nil {
		return out, err
	}
	// 已用、已过期一律拒绝，且措辞一致，不泄露是哪一种。
	if used != nil || !expires.After(now) {
		return out, httpx.NewError(httpx.ErrForbidden, "注册令牌无效")
	}
	var status string
	err = tx.QueryRow(c, `SELECT status FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrForbidden, "注册令牌无效")
	}
	if err != nil {
		return out, err
	}
	// 已下线的节点不得注册回来——吊销之后必须由后台重新签发令牌并改回状态。
	if status == "DISABLED" {
		return out, httpx.NewError(httpx.ErrForbidden, "该节点已被停用")
	}

	certDER, notAfter, err := s.signNodeCertificate(pub, nodeID, now)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(certDER)
	fingerprint := hex.EncodeToString(sum[:])

	if _, err = tx.Exec(c, `UPDATE node_enroll_tokens SET used_at=$2 WHERE id=$1 AND used_at IS NULL`,
		tokenID, now); err != nil {
		return out, err
	}
	// 指纹与证书同一事务：不会出现「证书发出去了但控制面不认」的中间态。
	if _, err = tx.Exec(c, `UPDATE nodes SET agent_cert_fingerprint=$2,agent_version=$3,updated_at=$4
 WHERE id=$1`, nodeID, fingerprint, in.AgentVersion, now); err != nil {
		return out, err
	}
	if _, err = tx.Exec(c, `INSERT INTO node_enrollments(node_id,enroll_token_id,cert_fingerprint,
 cert_not_after,wg_public_key,reality_public_key,agent_version,enrolled_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, nodeID, tokenID, fingerprint, notAfter,
		in.WGPublicKey, in.RealityPublicKey, in.AgentVersion, now); err != nil {
		return out, err
	}
	if err = tx.Commit(c); err != nil {
		return out, err
	}
	return EnrollResult{
		NodeID:        strconv.FormatInt(nodeID, 10),
		CertPEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		CAChainPEM:    s.EnrollCA.ChainPE,
		CertExpiresAt: notAfter.UTC().Format(time.RFC3339Nano),
	}, nil
}

// signNodeCertificate 按控制面自己的模板签发，CSR 里除公钥外的内容一律不采用。
func (s *Service) signNodeCertificate(pub crypto.PublicKey, nodeID int64, now time.Time) ([]byte, time.Time, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, time.Time{}, err
	}
	if serial.Sign() == 0 {
		serial = big.NewInt(1)
	}
	notAfter := now.Add(enrollCertTTL)
	// 永远不签超过 CA 自身有效期的证书。
	if !notAfter.Before(s.EnrollCA.Cert.NotAfter) {
		notAfter = s.EnrollCA.Cert.NotAfter
	}
	notBefore := now.Add(-enrollClockSkew)
	if !notBefore.After(s.EnrollCA.Cert.NotBefore) {
		notBefore = s.EnrollCA.Cert.NotBefore
	}
	if !notBefore.Before(notAfter) {
		return nil, time.Time{}, httpx.NewError(httpx.ErrMaintenance, "注册 CA 剩余有效期不足")
	}
	uri := &url.URL{Scheme: "spiffe", Host: "yuhai.internal", Path: "/node/" + strconv.FormatInt(nodeID, 10)}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "node-" + strconv.FormatInt(nodeID, 10)},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, s.EnrollCA.Cert, pub, s.EnrollCA.Key)
	if err != nil {
		return nil, time.Time{}, err
	}
	// 自检：签出来的东西必须能被本控制面自己的身份校验接受，否则不要发出去。
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, time.Time{}, err
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: s.Roots, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, time.Time{}, httpx.NewError(httpx.ErrMaintenance, "签发的证书未通过自检")
	}
	return der, notAfter, nil
}

// ---------- 证书续期 ----------

type RenewInput struct {
	CSRPEM string `json:"csr_pem"`
}

type RenewResult struct {
	CertPEM       string `json:"cert_pem"`
	CertExpiresAt string `json:"cert_expires_at"`
}

// 续期的重叠窗口：换绑之后，旧指纹在这段时间内仍被接受。
// 存在的唯一理由是「响应在回程丢了」——没有它，节点会从此连不上，只能人工重新注册。
const renewOverlap = 24 * time.Hour

// 到了证书有效期的这个比例才允许续期。太早续等于允许无限量铸造证书。
const renewAfterFraction = 2.0 / 3.0

// Renew 用当前证书换一张新的（方案 node.yaml 的 POST /node/v1/cert/renew）。
//
// 与注册的关键差别：这里**必须**有有效的客户端证书，身份完全来自 mTLS，没有令牌参与。
// 因此这条路径不能被用来获得一个新身份——它只能延长已有身份的寿命。
func (s *Service) Renew(ctx context.Context, state *tls.ConnectionState, raw []byte) (RenewResult, error) {
	var out RenewResult
	if s.EnrollCA == nil {
		return out, httpx.NewError(httpx.ErrNotFound, "节点证书续期未启用")
	}
	now := s.Clock.Now()
	id, err := certificateIdentity(state, s.Roots, now)
	if err != nil {
		return out, err
	}
	var in RenewInput
	if len(raw) == 0 || len(raw) > 32*1024 || nodewire.StrictJSON(raw, &in) != nil {
		return out, badEnroll()
	}
	pub, err := parseCSR(in.CSRPEM)
	if err != nil {
		return out, err
	}
	// 必须真的快到期了才续。用本次出示的证书自身的有效期算，不看库里记的。
	leaf := state.PeerCertificates[0]
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	if life <= 0 || now.Sub(leaf.NotBefore) < time.Duration(float64(life)*renewAfterFraction) {
		return out, httpx.NewError(httpx.ErrConflict, "尚未到续期时间")
	}

	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = tx.Rollback(stop)
	}()

	var status string
	var cert nodeCert
	err = tx.QueryRow(c, `SELECT status,`+nodeCertColumns+` FROM nodes WHERE id=$1 FOR UPDATE`, id.id).
		Scan(&status, &cert.Current, &cert.Prev, &cert.Until)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, forbidden()
	}
	if err != nil {
		return out, err
	}
	if status == "DISABLED" || !cert.accepts(id.fingerprint, now) {
		return out, forbidden()
	}

	certDER, notAfter, err := s.signNodeCertificate(pub, id.id, now)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(certDER)
	fingerprint := hex.EncodeToString(sum[:])

	// 关键一步：把旧指纹放进重叠窗口。
	//
	// 若本次出示的就是**当前**证书，说明上一次续期（如果有）已经生效，旧的那张可以退位，
	// 由这张当前证书接替 prev 的位置。
	//
	// 若出示的是 prev（说明节点没收到上一次续期的响应，还在用更早那张），则 prev 保持不变、
	// 截止时间也不延长——否则节点手里那张会被挤掉，反而弄巧成拙；窗口内它可以一直重试。
	prev, until := cert.Prev, cert.Until
	if cert.Current != nil && *cert.Current == id.fingerprint {
		prev = cert.Current
		overlap := now.Add(renewOverlap)
		// 旧证书本身到期后就不必再接受了。
		if leaf.NotAfter.Before(overlap) {
			overlap = leaf.NotAfter
		}
		until = &overlap
	}
	if _, err = tx.Exec(c, `UPDATE nodes SET agent_cert_fingerprint=$2,agent_cert_prev_fingerprint=$3,
 agent_cert_prev_until=$4,updated_at=$5 WHERE id=$1`, id.id, fingerprint, prev, until, now); err != nil {
		return out, err
	}
	if _, err = tx.Exec(c, `INSERT INTO node_enrollments(node_id,enroll_token_id,cert_fingerprint,cert_not_after,
 wg_public_key,reality_public_key,agent_version,enrolled_at,kind)
 SELECT $1,NULL,$2,$3,e.wg_public_key,e.reality_public_key,e.agent_version,$4,'RENEW'
 FROM node_enrollments e WHERE e.node_id=$1 ORDER BY e.enrolled_at DESC LIMIT 1`,
		id.id, fingerprint, notAfter, now); err != nil {
		return out, err
	}
	if err = tx.Commit(c); err != nil {
		return out, err
	}
	return RenewResult{
		CertPEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})),
		CertExpiresAt: notAfter.UTC().Format(time.RFC3339Nano),
	}, nil
}
