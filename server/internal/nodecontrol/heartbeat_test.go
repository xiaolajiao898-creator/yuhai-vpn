package nodecontrol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct{ nanos atomic.Int64 }

func newClock() *testClock {
	c := &testClock{}
	c.nanos.Store(time.Date(2032, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano())
	return c
}
func (c *testClock) Now() time.Time      { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *testClock) Add(d time.Duration) { c.nanos.Add(int64(d)) }
func (c *testClock) set(t time.Time)     { c.nanos.Store(t.UnixNano()) }

func heartbeatAt(c *testClock, seq string) Heartbeat {
	return Heartbeat{Sequence: seq, SentAt: c.Now().Format(time.RFC3339Nano), AgentVersion: "1.0.0", ConfigVersion: "0", SnapshotChecksum: "", OnlineCount: 12, CPU: 30, Memory: 20, NetRX: "9007199254740993", NetTX: "0", Kernel: "Linux 6.8"}
}

func TestHeartbeatStrictJSONAndNumericBoundaries(t *testing.T) {
	c := newClock()
	h := heartbeatAt(c, "1")
	raw, _ := json.Marshal(h)
	for _, tc := range []struct{ name, body string }{
		{"good", string(raw)}, {"duplicate", strings.TrimSuffix(string(raw), "}") + `,"sequence":"1"}`},
		{"case alias", strings.Replace(string(raw), `"cpu"`, `"CPU"`, 1)},
		{"unknown identity", strings.TrimSuffix(string(raw), "}") + `,"node_id":"1"}`},
		{"two bodies", string(raw) + string(raw)}, {"array", "[]"}, {"null", "null"}, {"oversized", string(raw) + strings.Repeat(" ", 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://node.test/node/v1/heartbeat", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			_, err := decodeHeartbeat(r, httptest.NewRecorder())
			if (err == nil) != (tc.name == "good") {
				t.Fatal("unexpected decoder result", err)
			}
		})
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for key := range fields {
		if key == "kernel" {
			continue
		}
		t.Run("required "+key, func(t *testing.T) {
			for _, null := range []bool{false, true} {
				copyFields := map[string]any{}
				for k, v := range fields {
					copyFields[k] = v
				}
				delete(copyFields, key)
				if null {
					copyFields[key] = nil
				}
				b, _ := json.Marshal(copyFields)
				r := httptest.NewRequest("POST", "https://node.test/node/v1/heartbeat", strings.NewReader(string(b)))
				r.Header.Set("Content-Type", "application/json")
				if _, err := decodeHeartbeat(r, httptest.NewRecorder()); err == nil {
					t.Fatal("missing/null field accepted")
				}
			}
		})
	}
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"sequence", "0"}, {"sequence", "01"}, {"sequence", "9223372036854775808"}, {"sequence", 12}, {"config_version", "-1"}, {"net_rx_bps", "1e3"}, {"net_tx_bps", "1.0"}, {"online_count", -1}, {"online_count", 1.5}, {"online_count", 10000001}, {"cpu", 101}, {"cpu", -1}, {"cpu", 1.5}, {"mem", 101}, {"snapshot_checksum", "abc"}, {"agent_version", "abc\n"}, {"kernel", ""}, {"sent_at", "2032-01-02T03:04:05+00:00"}, {"sent_at", "2032-01-02T03:04:05.0000001Z"},
	} {
		t.Run(tc.key+" invalid", func(t *testing.T) {
			copyFields := map[string]any{}
			for k, v := range fields {
				copyFields[k] = v
			}
			copyFields[tc.key] = tc.value
			b, _ := json.Marshal(copyFields)
			r := httptest.NewRequest("POST", "https://node.test/node/v1/heartbeat", strings.NewReader(string(b)))
			r.Header.Set("Content-Type", "application/json")
			if _, err := decodeHeartbeat(r, httptest.NewRecorder()); err == nil {
				t.Fatal("bad value accepted")
			}
		})
	}
	for _, kind := range []string{"query", "compression", "type", "duplicate type", "parameter"} {
		t.Run(kind, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://node.test/node/v1/heartbeat", strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			switch kind {
			case "query":
				r.URL.RawQuery = "node_id=1"
			case "compression":
				r.Header.Set("Content-Encoding", "gzip")
			case "type":
				r.Header.Set("Content-Type", "text/plain")
			case "duplicate type":
				r.Header.Add("Content-Type", "application/json")
			case "parameter":
				r.Header.Set("Content-Type", "application/json;version=1")
			}
			if _, err := decodeHeartbeat(r, httptest.NewRecorder()); err == nil {
				t.Fatal("bad transport accepted")
			}
		})
	}
	a, _ := validate(h)
	h.SentAt = "2032-01-02T03:04:05.000000Z"
	b, _ := validate(h)
	if a.hash != b.hash {
		t.Fatal("equivalent timestamps must have one canonical digest")
	}
	h.Sequence = "9223372036854775807"
	h.ConfigVersion = h.Sequence
	h.NetTX = h.Sequence
	if _, err := validate(h); err != nil {
		t.Fatal("int64 maximum rejected", err)
	}
}

type testCA struct {
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
	roots *x509.CertPool
	pem   []byte
	clock *testClock
}

func newCA(t *testing.T, c *testClock) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Ephemeral node fixture CA"}, NotBefore: c.Now().Add(-time.Hour), NotAfter: c.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &testCA{cert, key, roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), c}
}
func (ca *testCA) issue(t *testing.T, uri string, mutate func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ignored"}, NotBefore: ca.clock.Now().Add(-time.Minute), NotAfter: ca.clock.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if uri != "" {
		u, err := url.Parse(uri)
		if err != nil {
			t.Fatal(err)
		}
		tmpl.URIs = []*url.URL{u}
	}
	if mutate != nil {
		mutate(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}
func (ca *testCA) state(cert tls.Certificate) *tls.ConnectionState {
	return &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, PeerCertificates: []*x509.Certificate{cert.Leaf}, VerifiedChains: [][]*x509.Certificate{{cert.Leaf, ca.cert}}}
}
func (ca *testCA) serverFiles(t *testing.T) (string, string, string) {
	t.Helper()
	cert := ca.issue(t, "", func(c *x509.Certificate) {
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	})
	dir := t.TempDir()
	pub := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{pub: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), caFile: ca.pem} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return pub, keyFile, caFile
}

func TestHeartbeatCertificateIdentity(t *testing.T) {
	c := newClock()
	ca := newCA(t, c)
	for _, uri := range []string{"spiffe://yuhai.internal/node/1", "spiffe://yuhai.internal/node/9007199254740993", "spiffe://yuhai.internal/node/9223372036854775807"} {
		cert := ca.issue(t, uri, nil)
		if _, err := certificateIdentity(ca.state(cert), ca.roots, c.Now()); err != nil {
			t.Fatal("valid ID rejected", err)
		}
	}
	for _, uri := range []string{"", "spiffe://yuhai.internal/", "spiffe://yuhai.internal/node/", "spiffe://yuhai.internal/node/0", "spiffe://yuhai.internal/node/01", "spiffe://yuhai.internal/node/9223372036854775808", "spiffe://yuhai.internal/node/1?", "spiffe://yuhai.internal/node/1#x", "spiffe://other/node/1", "spiffe://yuhai.internal/node/%31", "spiffe://user@yuhai.internal/node/1", "https://yuhai.internal/node/1"} {
		t.Run(uri, func(t *testing.T) {
			cert := ca.issue(t, uri, nil)
			if _, err := certificateIdentity(ca.state(cert), ca.roots, c.Now()); err == nil {
				t.Fatal("bad URI identity accepted")
			}
		})
	}
	for _, mode := range []string{"expired", "future", "server only", "no EKU", "multiple SAN", "other CA", "unverified", "no TLS", "TLS12", "CA leaf"} {
		t.Run(mode, func(t *testing.T) {
			cert := ca.issue(t, "spiffe://yuhai.internal/node/1", func(x *x509.Certificate) {
				switch mode {
				case "expired":
					x.NotAfter = c.Now().Add(-time.Second)
				case "future":
					x.NotBefore = c.Now().Add(time.Second)
				case "server only":
					x.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
				case "no EKU":
					x.ExtKeyUsage = nil
				case "multiple SAN":
					x.URIs = append(x.URIs, x.URIs[0])
				case "CA leaf":
					x.IsCA = true
					x.BasicConstraintsValid = true
				}
			})
			state := ca.state(cert)
			roots := ca.roots
			switch mode {
			case "other CA":
				roots = newCA(t, c).roots
			case "unverified":
				state.VerifiedChains = nil
			case "no TLS":
				state = nil
			case "TLS12":
				state.Version = tls.VersionTLS12
			}
			if _, err := certificateIdentity(state, roots, c.Now()); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	cert := ca.issue(t, "spiffe://yuhai.internal/node/1", nil)
	state := ca.state(cert)
	c.Add(2 * time.Hour)
	if _, err := certificateIdentity(state, ca.roots, c.Now()); err == nil {
		t.Fatal("expired keepalive identity accepted")
	}
}

func TestHeartbeatServerTLSConfiguration(t *testing.T) {
	c := newClock()
	ca := newCA(t, c)
	cert, key, root := ca.serverFiles(t)
	srv, err := NewServer("127.0.0.1:18443", cert, key, root, nil, c)
	if err != nil || srv.TLSConfig.MinVersion != tls.VersionTLS13 || srv.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 {
		t.Fatal("unsafe TLS server", err)
	}
	for _, mode := range []string{"missing key", "empty CA", "junk CA", "leaf CA", "expired server"} {
		t.Run(mode, func(t *testing.T) {
			pub, priv, roots := ca.serverFiles(t)
			switch mode {
			case "missing key":
				priv += ".missing"
			case "empty CA":
				_ = os.WriteFile(roots, nil, 0600)
			case "junk CA":
				_ = os.WriteFile(roots, append(ca.pem, []byte("garbage")...), 0600)
			case "leaf CA":
				b, _ := os.ReadFile(pub)
				_ = os.WriteFile(roots, b, 0600)
			case "expired server":
				c.Add(2 * time.Hour)
				defer c.Add(-2 * time.Hour)
			}
			if _, err := NewServer("127.0.0.1:18443", pub, priv, roots, nil, c); err == nil {
				t.Fatal("invalid TLS files accepted")
			}
		})
	}
}
