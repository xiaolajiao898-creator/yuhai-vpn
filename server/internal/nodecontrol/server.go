package nodecontrol

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
)

type ServerOptions struct {
	WireGuardManifestEnabled bool
	TrafficReportEnabled     bool
	// 节点注册。为空时不加载签发 CA，/node/v1/enroll 不注册，监听器继续强制客户端证书。
	EnrollCACert, EnrollCAKey string
}

// NewServer validates all TLS inputs before a listening socket is opened. It
// never accepts proxy-supplied identities and has no plaintext fallback.
func NewServer(addr, certFile, keyFile, caFile string, pool *pgxpool.Pool, clk clock.Clock, options ...ServerOptions) (*http.Server, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("节点控制面服务端证书或密钥无效")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || leaf.IsCA || clk.Now().Before(leaf.NotBefore) || !clk.Now().Before(leaf.NotAfter) {
		return nil, errors.New("节点控制面服务端证书无效或已过期")
	}
	serverUsage := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			serverUsage = true
		}
	}
	if !serverUsage || (len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0) {
		return nil, errors.New("服务端证书缺少服务端用途或名称")
	}
	raw, err := os.ReadFile(caFile)
	if err != nil || len(raw) > 1024*1024 {
		return nil, errors.New("节点专用CA文件无效")
	}
	roots := x509.NewCertPool()
	count := 0
	for len(bytes.TrimSpace(raw)) > 0 {
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("节点CA必须只含证书")
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("节点CA格式无效")
		}
		ca, e := x509.ParseCertificate(block.Bytes)
		if e != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || clk.Now().Before(ca.NotBefore) || !clk.Now().Before(ca.NotAfter) {
			return nil, errors.New("节点CA无效或已过期")
		}
		roots.AddCert(ca)
		count++
		raw = rest
	}
	if count == 0 {
		return nil, errors.New("节点CA不能为空")
	}
	if len(options) > 1 {
		return nil, errors.New("invalid server options")
	}
	svc := &Service{Pool: pool, Clock: clk, Roots: roots}
	clientAuth := tls.RequireAndVerifyClientCert
	if len(options) == 1 {
		svc.WireGuardManifestEnabled = options[0].WireGuardManifestEnabled
		svc.TrafficReportEnabled = options[0].TrafficReportEnabled
		if options[0].EnrollCACert != "" || options[0].EnrollCAKey != "" {
			ca, e := LoadEnrollCA(options[0].EnrollCACert, options[0].EnrollCAKey, roots, clk.Now())
			if e != nil {
				return nil, e
			}
			svc.EnrollCA = ca
			// 只在启用注册时放宽到「给了证书就必须验得过，可以不给」——
			// 节点第一次来的时候手上还没有证书。其余路由仍各自调用 certificateIdentity，
			// 没有证书一律 403，所以放宽的只是握手，不是授权。
			clientAuth = tls.VerifyClientCertIfGiven
		}
	}
	// The node API is mTLS-only, but response hardening still matters for
	// misrouted clients and future endpoints.
	baseHandler := svc.Handler()
	securedHandler := securityHeaders(baseHandler)
	return &http.Server{
		Addr: addr, Handler: securedHandler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 45 * time.Second, MaxHeaderBytes: 16 * 1024,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: clientAuth, ClientCAs: roots.Clone(), Certificates: []tls.Certificate{cert}, Time: clk.Now},
	}, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
