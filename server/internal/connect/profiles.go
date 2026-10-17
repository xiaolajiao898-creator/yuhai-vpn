// Package connect 按设备凭据与节点入站实时组装连接材料（方案 11.x：POST /v1/connect/profiles）。
//
// 这是全项目唯一会把「能连上」所需材料交给客户端的地方，因此边界写死在这里：
//
//  1. 只下发**本设备自己的**材料。凭据按 device_id 精确取，绝不按用户遍历；
//     设备私钥从不存在于服务端，WireGuard 出站只在设备已上传公钥时才可能返回。
//  2. 只下发客户端声明支持、且节点**当前确实就绪**的协议。节点是否就绪以心跳上报的
//     ready_protocols 为准，与 App 节点列表同一套门槛，不另立标准。
//  3. 权益不足、等级不够、流量用完、设备被移除，一律拒绝并给出明确错误码，
//     不退化成「返回空列表」——空列表会被客户端当成节点问题而不断重试。
//  4. 本轮只实现 WIREGUARD 出站。其余协议即便节点声明就绪，也按 PROTOCOL_UNSUPPORTED
//     返回，绝不编造字段——宁可明说没有，也不给一份连不上的配置。
package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// Lifetime 是连接材料的有效期。材料本身不是授权凭证——真正的授权在节点侧由
// peer 清单决定——但仍然限定时长，避免客户端长期缓存已失效的配置。
const Lifetime = 5 * time.Minute

const maxNodes = 5

type Input struct {
	NodeIDs   []string `json:"node_ids"`
	Protocols []string `json:"protocols"`
}

type Outbound struct {
	Protocol        string   `json:"protocol"`
	Endpoint        string   `json:"endpoint"`
	ServerPublicKey string   `json:"server_public_key"`
	Address         string   `json:"address"`
	DNS             []string `json:"dns"`
	MTU             int      `json:"mtu"`
}

type Profile struct {
	NodeID    string     `json:"node_id"`
	Outbounds []Outbound `json:"outbounds"`
}

type Unavailable struct {
	NodeID string `json:"node_id"`
	Reason string `json:"reason"`
}

type Profiles struct {
	ExpiresAt   string        `json:"expires_at"`
	Profiles    []Profile     `json:"profiles"`
	Unavailable []Unavailable `json:"unavailable"`
}

type Service struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
	// Cipher 解密出口凭据（字段加密）。为 nil 时出口下发一律 50001，WireGuard 路径不受影响。
	Cipher *fieldenc.Cipher
}

var supportedProtocols = map[string]bool{
	"VLESS_REALITY": true, "TROJAN": true, "SHADOWSOCKS_2022": true, "WIREGUARD": true, "HYSTERIA2": true,
}

// 本轮真正能组装出可用配置的协议。
var implemented = map[string]bool{"WIREGUARD": true}

var certificatePin = regexp.MustCompile(`^[a-f0-9]{64}$`)

func badRequest(msg string) error { return httpx.NewError(httpx.ErrBadRequest, msg) }

func normalize(in Input) ([]int64, map[string]bool, error) {
	if len(in.NodeIDs) < 1 || len(in.NodeIDs) > maxNodes {
		return nil, nil, badRequest("一次最多请求 5 个节点")
	}
	if len(in.Protocols) < 1 || len(in.Protocols) > 8 {
		return nil, nil, badRequest("协议列表无效")
	}
	ids := make([]int64, 0, len(in.NodeIDs))
	seen := map[int64]bool{}
	for _, raw := range in.NodeIDs {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw || seen[id] {
			return nil, nil, badRequest("节点编号无效或重复")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	wanted := map[string]bool{}
	for _, p := range in.Protocols {
		if !supportedProtocols[p] || wanted[p] {
			return nil, nil, badRequest("协议无效或重复")
		}
		wanted[p] = true
	}
	return ids, wanted, nil
}

type rights struct {
	tier       int
	canConnect bool
	quotaLeft  bool
	until      time.Time
}

// entitlement 复用方案 5.2 的可连接判定，与节点清单、App 列表同一口径。
func entitlement(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) (rights, error) {
	var r rights
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1`, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "ACTIVE") {
		return r, httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	if err != nil {
		return r, err
	}
	var subTier, packTier int
	var subValid, subQuota, packValid bool
	var subUntil, packUntil *time.Time
	err = tx.QueryRow(ctx, `
 SELECT COALESCE((SELECT tier FROM subscriptions WHERE user_id=$1 AND expire_at>$2),0),
        EXISTS(SELECT 1 FROM subscriptions WHERE user_id=$1 AND expire_at>$2),
        EXISTS(SELECT 1 FROM subscriptions WHERE user_id=$1 AND expire_at>$2
                AND (period_quota_bytes IS NULL OR period_used_bytes<period_quota_bytes)),
        (SELECT expire_at FROM subscriptions WHERE user_id=$1 AND expire_at>$2),
        COALESCE((SELECT max(tier) FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes),0),
        EXISTS(SELECT 1 FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes),
        (SELECT max(expire_at) FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes)`,
		userID, now).Scan(&subTier, &subValid, &subQuota, &subUntil, &packTier, &packValid, &packUntil)
	if err != nil {
		return r, err
	}
	r.tier = subTier
	if packTier > r.tier {
		r.tier = packTier
	}
	r.quotaLeft = subQuota || packValid
	r.canConnect = r.quotaLeft
	switch {
	case !subValid && !packValid && subTier == 0 && packTier == 0:
		// 完全没有套餐或流量包
		return r, httpx.NewError(httpx.ErrNoPlan, "没有可用套餐")
	case !r.canConnect:
		return r, httpx.NewError(httpx.ErrQuotaExhausted, "流量已用完")
	}
	r.until = now.Add(Lifetime)
	for _, t := range []*time.Time{subUntil, packUntil} {
		if t != nil && t.After(now) && t.Before(r.until) {
			r.until = *t
		}
	}
	return r, nil
}

type device struct {
	address string
	pubkey  string
}

// deviceMaterial 按 device_id 精确取本设备凭据；绝不按用户遍历，也绝不读取密文口令。
func deviceMaterial(ctx context.Context, tx pgx.Tx, userID, deviceID int64) (device, error) {
	var d device
	var address, pubkey *string
	var status string
	err := tx.QueryRow(ctx, `SELECT d.status,c.wg_address::text,c.wg_pubkey
 FROM devices d LEFT JOIN device_credentials c ON c.device_id=d.id AND c.status='ACTIVE'
 WHERE d.id=$1 AND d.user_id=$2`, deviceID, userID).Scan(&status, &address, &pubkey)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "ACTIVE") {
		return d, httpx.NewError(httpx.ErrDeviceRevoked, "设备已被移除")
	}
	if err != nil {
		return d, err
	}
	if address == nil || pubkey == nil {
		// 设备尚未上传公钥：这不是错误，只是没有 WireGuard 材料可下发。
		return d, nil
	}
	if !nodewire.ValidWGPublicKey(*pubkey) {
		return d, httpx.Internal
	}
	if _, err := netip.ParsePrefix(*address); err != nil {
		return d, httpx.Internal
	}
	d.address, d.pubkey = *address, *pubkey
	return d, nil
}

type nodeRow struct {
	id             int64
	host           string
	status         string
	minTier        int
	lastHeartbeat  *time.Time
	sentAt         *time.Time
	received       *time.Time
	pin            *string
	receiptPin     *string
	configVersion  *int64
	checksum       *string
	readyProtocols []string
	wgPort         *int
	wgParams       []byte
}

// available 复用 App 节点列表的候选门槛，不另立标准。
func (n nodeRow) available(tier int, now time.Time) string {
	switch {
	case tier < n.minTier || n.minTier <= 0:
		return "TIER"
	case n.status != "ONLINE":
		return "MAINTENANCE"
	case n.lastHeartbeat == nil || n.lastHeartbeat.After(now) || now.Sub(*n.lastHeartbeat) > 90*time.Second ||
		(n.sentAt != nil && (n.sentAt.After(now.Add(5*time.Second)) || now.Sub(*n.sentAt) > 90*time.Second)):
		return "OFFLINE"
	case n.sentAt == nil || n.pin == nil || !certificatePin.MatchString(*n.pin) || n.receiptPin == nil ||
		*n.pin != *n.receiptPin || n.received == nil || !n.received.Equal(*n.lastHeartbeat) ||
		n.configVersion == nil || *n.configVersion <= 0 || n.checksum == nil ||
		!certificatePin.MatchString(*n.checksum) || len(n.readyProtocols) == 0:
		return "OFFLINE"
	}
	return ""
}

func (s *Service) Profiles(ctx context.Context, userID, deviceID int64, in Input) (Profiles, error) {
	var out Profiles
	if userID <= 0 || deviceID <= 0 {
		return out, httpx.NewError(httpx.ErrTokenMissing, "缺少账户或设备身份")
	}
	ids, wanted, err := normalize(in)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(c)
	}()
	now := s.Clock.Now()
	right, err := entitlement(ctx, tx, userID, now)
	if err != nil {
		return out, err
	}
	self, err := deviceMaterial(ctx, tx, userID, deviceID)
	if err != nil {
		return out, err
	}

	rows, err := tx.Query(ctx, `
 SELECT n.id,n.host,n.status,g.min_tier,n.last_heartbeat_at,h.sent_at,h.received_at,
        n.agent_cert_fingerprint,h.cert_fingerprint,h.config_version,h.snapshot_checksum,
        COALESCE(h.ready_protocols,'[]'::jsonb),
        (SELECT i.port FROM node_inbounds i WHERE i.node_id=n.id AND i.protocol='WIREGUARD' AND i.status='ENABLED' ORDER BY i.priority,i.id LIMIT 1),
        (SELECT i.public_params FROM node_inbounds i WHERE i.node_id=n.id AND i.protocol='WIREGUARD' AND i.status='ENABLED' ORDER BY i.priority,i.id LIMIT 1)
   FROM nodes n JOIN node_groups g ON g.id=n.group_id
   LEFT JOIN node_heartbeats h ON h.node_id=n.id
  WHERE n.id = ANY($1::bigint[]) AND n.status <> 'DISABLED'`, ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	found := map[int64]nodeRow{}
	for rows.Next() {
		var n nodeRow
		var ready []byte
		if err := rows.Scan(&n.id, &n.host, &n.status, &n.minTier, &n.lastHeartbeat, &n.sentAt, &n.received,
			&n.pin, &n.receiptPin, &n.configVersion, &n.checksum, &ready, &n.wgPort, &n.wgParams); err != nil {
			return out, err
		}
		if err := json.Unmarshal(ready, &n.readyProtocols); err != nil || !nodewire.ValidProtocols(n.readyProtocols) {
			return out, httpx.NewError(httpx.ErrNodeUnavailable, "节点就绪协议无效")
		}
		found[n.id] = n
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()

	out = Profiles{ExpiresAt: right.until.UTC().Format(time.RFC3339), Profiles: []Profile{}, Unavailable: []Unavailable{}}
	for _, id := range ids {
		n, ok := found[id]
		if !ok {
			out.Unavailable = append(out.Unavailable, Unavailable{NodeID: strconv.FormatInt(id, 10), Reason: "MAINTENANCE"})
			continue
		}
		if reason := n.available(right.tier, now); reason != "" {
			out.Unavailable = append(out.Unavailable, Unavailable{NodeID: strconv.FormatInt(id, 10), Reason: reason})
			continue
		}
		outbounds, err := assemble(n, self, wanted)
		if err != nil {
			return Profiles{}, err
		}
		if len(outbounds) == 0 {
			out.Unavailable = append(out.Unavailable, Unavailable{NodeID: strconv.FormatInt(id, 10), Reason: "PROTOCOL_UNSUPPORTED"})
			continue
		}
		out.Profiles = append(out.Profiles, Profile{NodeID: strconv.FormatInt(id, 10), Outbounds: outbounds})
	}
	if err := tx.Commit(ctx); err != nil {
		return Profiles{}, err
	}
	if len(out.Profiles) == 0 {
		return Profiles{}, httpx.NewError(httpx.ErrNodeUnavailable, "所选节点当前都不可用")
	}
	return out, nil
}

type wgParams struct {
	Interface string   `json:"interface"`
	PublicKey string   `json:"public_key"`
	Version   string   `json:"config_version"`
	DNS       []string `json:"dns,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
}

// assemble 按节点公开入站参数与本设备材料组装出站配置。
// 缺任何一项就不返回该协议，绝不用默认值把配置「补全」成一份连不上的东西。
func assemble(n nodeRow, self device, wanted map[string]bool) ([]Outbound, error) {
	ready := map[string]bool{}
	for _, p := range n.readyProtocols {
		ready[p] = true
	}
	outbounds := []Outbound{}
	if !wanted["WIREGUARD"] || !ready["WIREGUARD"] || !implemented["WIREGUARD"] {
		return outbounds, nil
	}
	if self.pubkey == "" || self.address == "" || n.wgPort == nil || len(n.wgParams) == 0 {
		return outbounds, nil
	}
	if *n.wgPort < 1 || *n.wgPort > 65535 || len(n.wgParams) > 4096 {
		return nil, httpx.NewError(httpx.ErrNodeUnavailable, "节点入站资料不完整")
	}
	var params wgParams
	if nodewire.StrictJSON(n.wgParams, &params) != nil {
		return nil, httpx.NewError(httpx.ErrNodeUnavailable, "节点入站资料无效")
	}
	if !nodewire.ValidWGPublicKey(params.PublicKey) || params.PublicKey == self.pubkey {
		return nil, httpx.NewError(httpx.ErrNodeUnavailable, "节点公钥无效")
	}
	dns := params.DNS
	if dns == nil {
		dns = []string{}
	}
	for _, d := range dns {
		if _, err := netip.ParseAddr(d); err != nil {
			return nil, httpx.NewError(httpx.ErrNodeUnavailable, "节点 DNS 配置无效")
		}
	}
	mtu := params.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if mtu < 576 || mtu > 1500 {
		return nil, httpx.NewError(httpx.ErrNodeUnavailable, "节点 MTU 配置无效")
	}
	return append(outbounds, Outbound{
		Protocol:        "WIREGUARD",
		Endpoint:        n.host + ":" + strconv.Itoa(*n.wgPort),
		ServerPublicKey: params.PublicKey,
		Address:         self.address,
		DNS:             dns,
		MTU:             mtu,
	}), nil
}
