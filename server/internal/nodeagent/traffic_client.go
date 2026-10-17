package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

func (c *Client) TrafficURL() string {
	return strings.TrimSuffix(c.endpoint, "/heartbeat") + "/traffic"
}

// ReportTraffic 发送一次流量上报。只有返回 accepted（含 duplicate）才算送达；
// 其余情况调用方必须保留 pending 并原样重发同一个 report_id。
func (c *Client) ReportTraffic(ctx context.Context, p TrafficPending) (nodewire.TrafficReceipt, error) {
	var out nodewire.TrafficReceipt
	if now := c.clock.Now(); now.Before(c.cert.NotBefore) || !now.Before(c.cert.NotAfter) {
		return out, &DeliveryError{Kind: "certificate_expired"}
	}
	body, err := json.Marshal(nodewire.TrafficReport{
		ReportID:    p.ReportID,
		WindowStart: p.WindowStart,
		WindowEnd:   p.WindowEnd,
		Items:       p.Items,
	})
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TrafficURL(), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return out, &DeliveryError{Kind: "traffic_unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, &DeliveryError{Kind: "traffic_rejected", Code: resp.StatusCode}
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || resp.Header.Get("Content-Encoding") != "" {
		return out, &DeliveryError{Kind: "invalid_traffic_receipt"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return out, &DeliveryError{Kind: "invalid_traffic_receipt"}
	}
	var envelope struct {
		Data      nodewire.TrafficReceipt `json:"data"`
		RequestID string                  `json:"request_id"`
	}
	if strictJSON(raw, &envelope) != nil || envelope.RequestID == "" || len(envelope.RequestID) > 128 || !envelope.Data.Accepted {
		return out, &DeliveryError{Kind: "invalid_traffic_receipt"}
	}
	return envelope.Data, nil
}
