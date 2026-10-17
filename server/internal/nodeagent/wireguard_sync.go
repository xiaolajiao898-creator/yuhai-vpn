package nodeagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (c *Client) WireGuardURL() string {
	return strings.TrimSuffix(c.endpoint, "/heartbeat") + "/wireguard/manifest"
}
func (c *Client) FetchWireGuard(ctx context.Context) (nodewire.WGLease, error) {
	var out nodewire.WGLease
	if now := c.clock.Now(); now.Before(c.cert.NotBefore) || !now.Before(c.cert.NotAfter) {
		return out, &DeliveryError{Kind: "certificate_expired"}
	}
	nonceBytes := make([]byte, 16)
	if _, e := rand.Read(nonceBytes); e != nil {
		return out, errors.New("snapshot request nonce unavailable")
	}
	nonce := hex.EncodeToString(nonceBytes)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.WireGuardURL(), nil)
	if e != nil {
		return out, e
	}
	req.Header.Set("X-Snapshot-Nonce", nonce)
	req.Header.Set("Accept", "application/json")
	resp, e := c.client.Do(req)
	if e != nil {
		return out, &DeliveryError{Kind: "snapshot_unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, &DeliveryError{Kind: "snapshot_rejected", Code: resp.StatusCode}
	}
	media, _, e := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if e != nil || media != "application/json" || resp.Header.Get("Content-Encoding") != "" {
		return out, &DeliveryError{Kind: "invalid_snapshot"}
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1048577))
	if e != nil || len(raw) > 1048576 {
		return out, &DeliveryError{Kind: "invalid_snapshot"}
	}
	var envelope struct {
		Data      nodewire.WGLease `json:"data"`
		RequestID string           `json:"request_id"`
	}
	if strictJSON(raw, &envelope) != nil || envelope.RequestID == "" || len(envelope.RequestID) > 128 || envelope.Data.RequestNonce != nonce {
		return out, &DeliveryError{Kind: "invalid_snapshot"}
	}
	data, _ := json.Marshal(envelope.Data)
	out, e = nodewire.ParseWGLease(data, c.nodeID, c.clock.Now())
	if e != nil {
		return nodewire.WGLease{}, &DeliveryError{Kind: "invalid_snapshot"}
	}
	return out, nil
}
