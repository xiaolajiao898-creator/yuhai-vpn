// Package node 实现节点控制面（阶段二）与 App 侧节点列表（本文件，方案 8.2、11.4）。
// 列表不含凭证；当前等级无权使用的节点只返回展示信息，不返回主机地址。
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

type Probe struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type View struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	CountryCode   string   `json:"country_code"`
	City          string   `json:"city"`
	Region        string   `json:"region"`
	Available     bool     `json:"available"`
	LockedReason  *string  `json:"locked_reason"`
	Load          *int     `json:"load"`
	OnlineCount   *int     `json:"online_count,omitempty"`
	CapacityUsers *int     `json:"capacity_users,omitempty"`
	Saturated     *bool    `json:"saturated,omitempty"`
	IsRecommended bool     `json:"is_recommended"`
	Probe         *Probe   `json:"probe"`
	Protocols     []string `json:"protocols"`
}

type ListService struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

type candidate struct {
	view                            View
	id                              int64
	host, status                    string
	port, load, minTier             int
	online, capacity                int
	lastHeartbeat, received, sentAt *time.Time
	pin, receiptPin, checksum       *string
	configVersion                   *int64
	protocols                       []string
	readyProtocols                  []string
}

var certificatePin = regexp.MustCompile(`^[a-f0-9]{64}$`)

// A fresh receipt is a candidate-selection signal, not proof of tunnel execution.
func project(n candidate, tier int, now time.Time) (View, error) {
	v := n.view
	v.ID = strconv.FormatInt(n.id, 10)
	v.Protocols = []string{}
	v.OnlineCount = nil
	v.CapacityUsers = nil
	v.Saturated = nil
	if !nodewire.ValidProtocols(n.readyProtocols) {
		return v, httpx.NewError(httpx.ErrNodeUnavailable, "节点就绪协议无效")
	}
	if n.id <= 0 || n.load < 0 || n.load > 100 || n.minTier <= 0 || n.port < 1 || n.port > 65535 || n.host == "" || len(n.protocols) > 64 {
		return v, httpx.NewError(httpx.ErrNodeUnavailable, "节点资料不完整")
	}
	switch {
	case tier < n.minTier:
		v.LockedReason = strPtr("TIER")
	case n.status != "ONLINE":
		v.LockedReason = strPtr("MAINTENANCE")
	case n.lastHeartbeat == nil || n.lastHeartbeat.After(now) || now.Sub(*n.lastHeartbeat) > 90*time.Second || (n.sentAt != nil && (n.sentAt.After(now.Add(5*time.Second)) || now.Sub(*n.sentAt) > 90*time.Second)):
		v.LockedReason = strPtr("OFFLINE")
	case n.sentAt == nil || n.pin == nil || !certificatePin.MatchString(*n.pin) || n.receiptPin == nil || *n.pin != *n.receiptPin || n.received == nil || !n.received.Equal(*n.lastHeartbeat) || n.configVersion == nil || *n.configVersion <= 0 || n.checksum == nil || !certificatePin.MatchString(*n.checksum) || len(n.protocols) == 0 || len(n.readyProtocols) == 0:
		v.LockedReason = strPtr("NOT_READY")
	default:
		seen := map[string]bool{}
		for _, p := range n.protocols {
			switch p {
			case "SS2022":
				p = "SHADOWSOCKS_2022"
			case "VLESS_REALITY", "TROJAN", "WIREGUARD", "HYSTERIA2":
			default:
				return View{}, httpx.NewError(httpx.ErrNodeUnavailable, "节点协议无效")
			}
			for _, ready := range n.readyProtocols {
				if ready == p && !seen[p] {
					seen[p] = true
					v.Protocols = append(v.Protocols, p)
					break
				}
			}
		}
		if len(v.Protocols) == 0 {
			v.LockedReason = strPtr("NOT_READY")
			return v, nil
		}
		v.Available = true
		v.Load = &n.load
		online, capacity := n.online, n.capacity
		v.OnlineCount = &online
		v.CapacityUsers = &capacity
		saturated := capacity > 0 && online >= capacity
		v.Saturated = &saturated
		if saturated {
			v.IsRecommended = false
		}
		v.Probe = &Probe{Host: n.host, Port: n.port}
	}
	return v, nil
}

func listVersion(nodes []View) (string, error) {
	data, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "n-" + hex.EncodeToString(sum[:]), nil
}

func (s *ListService) List(ctx context.Context, userID int64) ([]View, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, "", err
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(c)
	}()
	rows, err := tx.Query(ctx, `SELECT n.id,n.name,n.country_code,n.city,n.region,n.host,n.probe_port,n.status,n.is_recommended,n.load_score,
  n.online_count,n.capacity_users,
  g.min_tier,n.last_heartbeat_at,n.agent_cert_fingerprint,h.received_at,h.sent_at,h.cert_fingerprint,h.config_version,h.snapshot_checksum,
  COALESCE(h.ready_protocols,'[]'::jsonb),
  ARRAY(SELECT i.protocol FROM node_inbounds i WHERE i.node_id=n.id AND i.status='ENABLED' ORDER BY i.priority,i.id LIMIT 65)
  FROM nodes n JOIN node_groups g ON g.id=n.group_id LEFT JOIN node_heartbeats h ON h.node_id=n.id
  WHERE n.status <> 'DISABLED' ORDER BY g.min_tier,n.sort_order,n.id LIMIT 1001`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	all := []candidate{}
	for rows.Next() {
		var n candidate
		err = rows.Scan(&n.id, &n.view.Name, &n.view.CountryCode, &n.view.City, &n.view.Region, &n.host, &n.port, &n.status, &n.view.IsRecommended, &n.load, &n.online, &n.capacity, &n.minTier, &n.lastHeartbeat, &n.pin, &n.received, &n.sentAt, &n.receiptPin, &n.configVersion, &n.checksum, &n.readyProtocols, &n.protocols)
		if err != nil {
			return nil, "", err
		}
		all = append(all, n)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()
	if len(all) > 1000 {
		return nil, "", httpx.NewError(httpx.ErrNodeUnavailable, "节点列表超出读取上限")
	}
	now := s.Clock.Now()
	var tier int
	err = tx.QueryRow(ctx, `SELECT GREATEST(
 COALESCE((SELECT tier FROM subscriptions WHERE user_id=$1 AND expire_at>$2),0),
 COALESCE((SELECT max(tier) FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes),0))
 FROM users WHERE id=$1 AND status='ACTIVE'`, userID, now).Scan(&tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	if err != nil {
		return nil, "", err
	}
	out := make([]View, 0, len(all))
	for _, n := range all {
		v, e := project(n, tier, now)
		if e != nil {
			return nil, "", e
		}
		out = append(out, v)
	}
	version, err := listVersion(out)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return out, version, nil
}

func strPtr(s string) *string { return &s }

type Handler struct {
	Svc *ListService
	MW  *auth.Middleware
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	dev := func(c *gin.Context) string { return strconv.FormatInt(httpx.DeviceID(c), 10) }
	v1.GET("/nodes", h.MW.S1(), h.MW.RateLimit("nodes_dev", 20, time.Minute, false, dev), h.list)
}

func (h *Handler) list(c *gin.Context) {
	nodes, version, err := h.Svc.List(c.Request.Context(), httpx.UserID(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	etag := `"` + version + `"`
	c.Header("Cache-Control", "private, no-cache")
	c.Header("Vary", "Authorization")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	httpx.OK(c, gin.H{"version": version, "nodes": nodes})
}
