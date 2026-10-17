package nodeagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

type Client struct {
	endpoint string
	nodeID   string
	client   *http.Client
	cert     *x509.Certificate
	clock    clock.Clock
}
type DeliveryError struct {
	Kind string
	Code int
}

func (e *DeliveryError) Error() string {
	return "heartbeat delivery: " + e.Kind + " (" + strconv.Itoa(e.Code) + ")"
}

func Endpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("control URL must be a plain HTTPS origin")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return "", errors.New("invalid control port")
		}
	}
	u.Path = "/node/v1/heartbeat"
	return u.String(), nil
}
func NewClient(origin, certFile, keyFile, caFile string, clk clock.Clock) (*Client, error) {
	endpoint, err := Endpoint(origin)
	if err != nil {
		return nil, err
	}
	keyBytes, err := readPrivate(keyFile, 65536)
	if err != nil {
		return nil, errors.New("node private key must be a private regular file")
	}
	certBytes, err := os.ReadFile(certFile)
	if err != nil || len(certBytes) > 1024*1024 {
		return nil, errors.New("node certificate unavailable")
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, errors.New("node certificate/key unavailable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid node certificate")
	}
	if leaf.IsCA || len(leaf.URIs) != 1 || clk.Now().Before(leaf.NotBefore) || !clk.Now().Before(leaf.NotAfter) {
		return nil, errors.New("node certificate identity/validity invalid")
	}
	const prefix = "spiffe://yuhai.internal/node/"
	value := leaf.URIs[0].String()
	id := strings.TrimPrefix(value, prefix)
	n, err := integer(id)
	if err != nil || n == 0 || value != prefix+strconv.FormatInt(n, 10) {
		return nil, errors.New("invalid node URI identity")
	}
	clientUsage := false
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			clientUsage = true
		}
	}
	if !clientUsage {
		return nil, errors.New("node certificate lacks client usage")
	}
	raw, err := os.ReadFile(caFile)
	if err != nil || len(raw) > 1024*1024 {
		return nil, errors.New("control CA unavailable")
	}
	roots := x509.NewCertPool()
	count := 0
	for len(bytes.TrimSpace(raw)) > 0 {
		if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, errors.New("invalid control CA contents")
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("invalid control CA")
		}
		c, e := x509.ParseCertificate(block.Bytes)
		if e != nil || !c.IsCA || !c.BasicConstraintsValid {
			return nil, errors.New("invalid control CA certificate")
		}
		roots.AddCert(c)
		count++
		raw = rest
	}
	if count == 0 {
		return nil, errors.New("empty control CA")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, Time: clk.Now}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 45 * time.Second, MaxIdleConnsPerHost: 1, DisableCompression: true}
	c := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{endpoint, id, c, leaf, clk}, nil
}
func (c *Client) NodeID() string { return c.nodeID }
func (c *Client) URL() string    { return c.endpoint }
func (c *Client) Close()         { c.client.CloseIdleConnections() }
func (c *Client) Send(ctx context.Context, h nodewire.Heartbeat) (nodewire.Receipt, error) {
	var out nodewire.Receipt
	if c.clock.Now().Before(c.cert.NotBefore) || !c.clock.Now().Before(c.cert.NotAfter) {
		return out, &DeliveryError{Kind: "certificate_expired"}
	}
	if err := nodewire.ValidateHeartbeat(h); err != nil {
		return out, &DeliveryError{Kind: "invalid_request"}
	}
	raw, _ := json.Marshal(h)
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		var verify *tls.CertificateVerificationError
		if errors.As(err, &verify) {
			return out, &DeliveryError{Kind: "tls_verification"}
		}
		return out, &DeliveryError{Kind: "transient"}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return out, &DeliveryError{Kind: "transient", Code: resp.StatusCode*100 + 1}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if err != nil || len(body) > 8192 {
		return out, &DeliveryError{Kind: "invalid_response"}
	}
	if resp.StatusCode != 200 {
		var envelope struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			RequestID string `json:"request_id"`
		}
		if strictJSON(body, &envelope) != nil || envelope.Error.Code/100 != resp.StatusCode {
			return out, &DeliveryError{Kind: "invalid_response"}
		}
		kind := "rejected"
		if envelope.Error.Code == 40105 {
			kind = "expired_request"
		}
		return out, &DeliveryError{Kind: kind, Code: envelope.Error.Code}
	}
	var envelope struct {
		Data      nodewire.Receipt `json:"data"`
		RequestID string           `json:"request_id"`
	}
	if strictJSON(body, &envelope) != nil {
		return out, &DeliveryError{Kind: "invalid_response"}
	}
	out = envelope.Data
	sent, _ := time.Parse(time.RFC3339Nano, h.SentAt)
	if envelope.RequestID == "" || !out.Accepted || out.Sequence != h.Sequence || out.Actions == nil || len(out.Actions) != 0 || out.ServerTime.IsZero() || out.ReceivedAt.IsZero() || out.ReceivedAt.After(out.ServerTime) || out.ReceivedAt.Before(sent.Add(-5*time.Second)) || out.ReceivedAt.After(sent.Add(60*time.Second)) {
		return nodewire.Receipt{}, &DeliveryError{Kind: "invalid_response"}
	}
	return out, nil
}
