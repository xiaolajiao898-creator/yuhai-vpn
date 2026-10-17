package nodecontrol

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

type wgGuard struct {
	GroupID int64
	Tier    int
}

func wgUnavailable() error {
	return httpx.NewError(httpx.ErrNodeUnavailable, "WireGuard配置尚未就绪")
}

// Outside the read snapshot: a revoked/rotated certificate must be rechecked live.
func (s *Service) wgGuard(ctx context.Context, state *tls.ConnectionState) (identity, wgGuard, error) {
	now := s.Clock.Now()
	id, e := certificateIdentity(state, s.Roots, now)
	if e != nil {
		return id, wgGuard{}, e
	}
	var status string
	var cert nodeCert
	var guard wgGuard
	e = s.Pool.QueryRow(ctx, `SELECT n.status,n.`+nodeCertColumns2+`,n.group_id,g.min_tier FROM nodes n JOIN node_groups g ON g.id=n.group_id WHERE n.id=$1`, id.id).
		Scan(&status, &cert.Current, &cert.Prev, &cert.Until, &guard.GroupID, &guard.Tier)
	if errors.Is(e, pgx.ErrNoRows) {
		return id, guard, forbidden()
	}
	if e != nil {
		return id, guard, e
	}
	if status != "ONLINE" || !cert.accepts(id.fingerprint, now) {
		return id, guard, forbidden()
	}
	if guard.Tier < 1 || guard.Tier > 9 {
		return id, guard, wgUnavailable()
	}
	return id, guard, nil
}

// wgDesiredState 在已有的读快照内构建某节点的公开清单并计算校验和。
// 节点取件接口与撤销回执对账共用本函数，确保两者对「某凭据是否在该节点上」
// 的判定完全一致——回执若基于另一套规则，就是假回执。
// expiry 会被收敛到所有相关权益到期时间中最早的那个。
func wgDesiredState(ctx context.Context, tx pgx.Tx, nodeID int64, tier int, issued, expiry time.Time) (nodewire.WGManifest, string, time.Time, error) {
	var manifest nodewire.WGManifest
	// Fixed public projection; never select proxy_secret_enc, auth tokens or device private material.
	rows, e := tx.Query(ctx, `SELECT port,public_params FROM node_inbounds WHERE node_id=$1 AND protocol='WIREGUARD' AND status='ENABLED' ORDER BY id LIMIT 2`, nodeID)
	if e != nil {
		return manifest, "", expiry, e
	}
	var params struct {
		Interface string `json:"interface"`
		PublicKey string `json:"public_key"`
		Version   string `json:"config_version"`
	}
	var port, count int
	var raw []byte
	for rows.Next() {
		count++
		if e = rows.Scan(&port, &raw); e != nil {
			break
		}
	}
	rows.Close()
	if e != nil {
		return manifest, "", expiry, e
	}
	if rows.Err() != nil {
		return manifest, "", expiry, rows.Err()
	}
	if count != 1 || len(raw) > 4096 || nodewire.StrictJSON(raw, &params) != nil {
		return manifest, "", expiry, wgUnavailable()
	}
	manifest = nodewire.WGManifest{Schema: 1, NodeID: strconv.FormatInt(nodeID, 10), ConfigVersion: params.Version, Interface: params.Interface, PublicKey: params.PublicKey, ListenPort: port, Peers: []nodewire.WGPeer{}}
	rows, e = tx.Query(ctx, `
 WITH rights AS (
 SELECT user_id,device_limit,expire_at FROM subscriptions
 WHERE tier >= $1 AND tier<=9 AND device_limit>0 AND expire_at>$2
 AND (period_quota_bytes IS NULL OR period_used_bytes<period_quota_bytes)
 UNION ALL
 SELECT user_id,device_limit,expire_at FROM traffic_packs
 WHERE tier >= $1 AND tier<=9 AND device_limit>0 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes
 ), eligible AS (
 SELECT user_id,max(device_limit) AS device_limit,min(expire_at) AS until FROM rights GROUP BY user_id
 ), addons AS (
 SELECT user_id,sum(quantity) AS extra,min(expire_at) AS until FROM device_addons WHERE expire_at>$2 GROUP BY user_id
 ), active_devices AS (
 SELECT user_id,count(*) AS count FROM devices WHERE status='ACTIVE' GROUP BY user_id
 )
 SELECT c.id,c.wg_pubkey,c.wg_address::text,LEAST(e.until,COALESCE(a.until,e.until))
 FROM device_credentials c JOIN devices d ON d.id=c.device_id JOIN users u ON u.id=d.user_id
 JOIN eligible e ON e.user_id=u.id JOIN active_devices n ON n.user_id=u.id LEFT JOIN addons a ON a.user_id=u.id
 WHERE u.status='ACTIVE' AND d.status='ACTIVE' AND c.status='ACTIVE'
 AND n.count <= e.device_limit+COALESCE(a.extra,0)
 AND (c.wg_pubkey IS NOT NULL OR c.wg_address IS NOT NULL)
 ORDER BY c.id LIMIT 1001`, tier, issued)
	if e != nil {
		return manifest, "", expiry, e
	}
	for rows.Next() {
		var cid int64
		var key, address *string
		var until time.Time
		if e = rows.Scan(&cid, &key, &address, &until); e != nil {
			break
		}
		if len(manifest.Peers) == 1000 || key == nil || address == nil {
			e = wgUnavailable()
			break
		}
		manifest.Peers = append(manifest.Peers, nodewire.WGPeer{CredentialID: strconv.FormatInt(cid, 10), PublicKey: *key, AllowedIPs: []string{*address}})
		if until.Before(expiry) {
			expiry = until
		}
	}
	rows.Close()
	if e != nil {
		return manifest, "", expiry, e
	}
	if rows.Err() != nil {
		return manifest, "", expiry, rows.Err()
	}
	encoded, _ := json.Marshal(manifest)
	manifest, digest, e := nodewire.ParseWGManifest(encoded, manifest.NodeID)
	if e != nil {
		return manifest, "", expiry, wgUnavailable()
	}
	return manifest, digest, expiry, nil
}

func (s *Service) WireGuardManifest(ctx context.Context, state *tls.ConnectionState, nonce string) (nodewire.WGLease, error) {
	var out nodewire.WGLease
	if !s.WireGuardManifestEnabled {
		return out, wgUnavailable()
	}
	if !nodewire.ValidWGNonce(nonce) {
		return out, httpx.NewError(httpx.ErrBadRequest, "清单请求标识无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id, guard, e := s.wgGuard(ctx, state)
	if e != nil {
		return out, e
	}
	issued := s.Clock.Now().UTC().Truncate(time.Microsecond)
	expiry := issued.Add(30 * time.Second)
	if state.PeerCertificates[0].NotAfter.Before(expiry) {
		expiry = state.PeerCertificates[0].NotAfter
	}
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(c)
	}()
	manifest, digest, expiry, e := wgDesiredState(ctx, tx, id.id, guard.Tier, issued, expiry)
	if e != nil {
		return out, e
	}
	// End the read transaction before the final live authorization; changes to
	// devices after the snapshot remain bounded by this lease, not instant revocation.
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	current, finalGuard, e := s.wgGuard(ctx, state)
	if e != nil {
		return out, e
	}
	if current != id || finalGuard != guard {
		return out, forbidden()
	}
	now := s.Clock.Now()
	if now.Before(issued) || !now.Before(expiry) {
		return out, wgUnavailable()
	}
	out = nodewire.WGLease{Schema: 1, RequestNonce: nonce, IssuedAt: issued.Format(time.RFC3339Nano), ExpiresAt: expiry.UTC().Format(time.RFC3339Nano), Checksum: digest, Manifest: manifest}
	encoded, _ := json.Marshal(out)
	if _, e = nodewire.ParseWGLease(encoded, manifest.NodeID, now); e != nil {
		return nodewire.WGLease{}, wgUnavailable()
	}
	return out, nil
}
