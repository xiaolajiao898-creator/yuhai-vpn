// Package nodecontrol is the private mTLS control plane, separate from App auth.
package nodecontrol

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/httpx"
)

type identity struct {
	id          int64
	fingerprint string
}

func forbidden() error { return httpx.NewError(httpx.ErrForbidden, "节点身份无效或已禁用") }

// A verified handshake alone is insufficient on a long-lived connection: verify
// the chain again at request/transaction time, then check the live DB pin.
func certificateIdentity(state *tls.ConnectionState, roots *x509.CertPool, now time.Time) (identity, error) {
	if state == nil || !state.HandshakeComplete || state.Version < tls.VersionTLS13 || roots == nil || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return identity{}, forbidden()
	}
	leaf := state.PeerCertificates[0]
	if len(state.VerifiedChains[0]) == 0 || !bytes.Equal(state.VerifiedChains[0][0].Raw, leaf.Raw) || leaf.IsCA || len(leaf.URIs) != 1 || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return identity{}, forbidden()
	}
	intermediates := x509.NewCertPool()
	for _, cert := range state.PeerCertificates[1:] {
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return identity{}, forbidden()
	}
	// Require explicit ClientAuth EKU; absent/Any EKU must not broaden trust.
	clientUsage := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			clientUsage = true
		}
	}
	if !clientUsage {
		return identity{}, forbidden()
	}
	u := leaf.URIs[0]
	const prefix = "spiffe://yuhai.internal/node/"
	if u == nil || u.Scheme != "spiffe" || u.Host != "yuhai.internal" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.ForceQuery {
		return identity{}, forbidden()
	}
	if !strings.HasPrefix(u.Path, "/node/") {
		return identity{}, forbidden()
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(u.Path, "/node/"), 10, 64)
	if err != nil || id <= 0 || u.String() != prefix+strconv.FormatInt(id, 10) {
		return identity{}, forbidden()
	}
	hash := sha256.Sum256(leaf.Raw)
	return identity{id, hex.EncodeToString(hash[:])}, nil
}

// nodeCert 是一台节点当前被接受的证书指纹，外加一个续期重叠窗口内仍被接受的旧指纹。
type nodeCert struct {
	Current *string
	Prev    *string
	Until   *time.Time
}

const nodeCertColumns = `agent_cert_fingerprint,agent_cert_prev_fingerprint,agent_cert_prev_until`

// accepts 判断本次连接出示的证书指纹是否属于这台节点。
//
// 三处调用（心跳、清单取件、流量上报）共用这一份实现：指纹比对是身份判定的最后一道，
// 各写一份迟早会在某一处漏掉重叠窗口或漏掉常数时间比较。
//
// 重叠窗口的存在只是为了让「续期响应丢了」不至于把节点永久锁死；窗口一过，
// 旧指纹立刻不再被接受，无需任何清理任务。
func (c nodeCert) accepts(presented string, now time.Time) bool {
	// A missing current binding is revocation, never a renewal overlap. This
	// also fails closed for legacy rows whose previous fingerprint was retained.
	if c.Current == nil || *c.Current == "" || presented == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(*c.Current), []byte(presented)) == 1 {
		return true
	}
	if c.Prev == nil || c.Until == nil || !c.Until.After(now) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(*c.Prev), []byte(presented)) == 1
}

// nodeCertColumns2 与 nodeCertColumns 同义，供带表别名的查询拼接使用。
const nodeCertColumns2 = `agent_cert_fingerprint,n.agent_cert_prev_fingerprint,n.agent_cert_prev_until`
