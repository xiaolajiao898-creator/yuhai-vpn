package nodecontrol

import (
	"context"

	"crypto/tls"
	"crypto/x509"

	"encoding/json"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

type Heartbeat = nodewire.Heartbeat
type Receipt = nodewire.Receipt

type Service struct {
	WireGuardManifestEnabled bool
	// 流量上报入库与权益扣减，默认关闭。关闭时该路由根本不注册。
	TrafficReportEnabled bool
	Pool                 *pgxpool.Pool
	Clock                clock.Clock
	Roots                *x509.CertPool
	// 节点注册签发 CA。为 nil 时 /node/v1/enroll 路由不注册，注册一律 404。
	EnrollCA *EnrollCA
}

func badHeartbeat() error { return httpx.NewError(httpx.ErrBadRequest, "心跳字段或格式无效") }
func conflict() error {
	return httpx.NewError(httpx.ErrConflict, "心跳序号或时间冲突，请核实持久化上报状态")
}

type validated struct {
	sequence, version, rx, tx int64
	sent                      time.Time
	hash                      string
}

func validate(h Heartbeat) (validated, error) {
	v, err := nodewire.Validate(h)
	if err != nil {
		return validated{}, badHeartbeat()
	}
	return validated{v.Sequence, v.Version, v.RX, v.TX, v.Sent, v.Hash}, nil
}
func ValidateHeartbeat(h Heartbeat) error { _, err := validate(h); return err }

// Accept serializes all heartbeats with node status/pin changes using the node
// row lock. Telemetry and the latest idempotency receipt commit atomically.
func (s *Service) Accept(ctx context.Context, state *tls.ConnectionState, h Heartbeat) (Receipt, error) {
	out := Receipt{}
	id, err := certificateIdentity(state, s.Roots, s.Clock.Now())
	if err != nil {
		return out, err
	}
	v, err := validate(h)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(cleanup)
	}()
	var status string
	var cert nodeCert
	var capacity int64
	var lastHeartbeat *time.Time
	err = tx.QueryRow(ctx, `SELECT status,`+nodeCertColumns+`,capacity_users,last_heartbeat_at FROM nodes WHERE id=$1 FOR UPDATE`, id.id).
		Scan(&status, &cert.Current, &cert.Prev, &cert.Until, &capacity, &lastHeartbeat)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, forbidden()
	}
	if err != nil {
		return out, err
	}
	now := s.Clock.Now().UTC().Truncate(time.Microsecond)
	if _, err = certificateIdentity(state, s.Roots, now); err != nil {
		return out, err
	}
	if status == "DISABLED" || !cert.accepts(id.fingerprint, now) {
		return out, forbidden()
	}
	var previous int64
	var hash string
	var received, sent time.Time
	err = tx.QueryRow(ctx, `SELECT sequence,payload_hash,received_at,sent_at FROM node_heartbeats WHERE node_id=$1`, id.id).Scan(&previous, &hash, &received, &sent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil && v.sequence <= previous {
		if v.sequence != previous || hash != v.hash {
			return out, conflict()
		}
		return Receipt{Accepted: true, Duplicate: true, Sequence: h.Sequence, ReceivedAt: received, ServerTime: now, Actions: []string{}}, nil
	}
	if v.sent.Before(now.Add(-60*time.Second)) || v.sent.After(now.Add(5*time.Second)) {
		return out, httpx.NewError(httpx.ErrTimestampSkew, "心跳时间超出允许范围")
	}
	if (!sent.IsZero() && v.sent.Before(sent)) || (lastHeartbeat != nil && now.Before(*lastHeartbeat)) {
		return out, conflict()
	}
	if previous > 0 && now.Sub(received) < 5*time.Second {
		return out, httpx.NewError(httpx.ErrRateLimited, "心跳过于频繁，请保持30秒周期")
	}
	// No configured bandwidth capacity yet: load is max(CPU, connection ratio),
	// never an invented bandwidth utilization or a verified kernel readiness.
	// capacity <= 0 means "no connection cap", so the ratio is not computed.
	load := loadScore(h.OnlineCount, capacity, h.CPU)
	protocols := h.ReadyProtocols
	if protocols == nil {
		protocols = []string{}
	}
	protocolJSON, _ := json.Marshal(protocols)
	_, err = tx.Exec(ctx, `INSERT INTO node_heartbeats(node_id,sequence,payload_hash,sent_at,received_at,config_version,snapshot_checksum,cpu,mem,net_rx_bps,net_tx_bps,kernel,cert_fingerprint,ready_protocols)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),$13,$14::jsonb)
 ON CONFLICT(node_id) DO UPDATE SET sequence=EXCLUDED.sequence,payload_hash=EXCLUDED.payload_hash,sent_at=EXCLUDED.sent_at,received_at=EXCLUDED.received_at,config_version=EXCLUDED.config_version,snapshot_checksum=EXCLUDED.snapshot_checksum,cpu=EXCLUDED.cpu,mem=EXCLUDED.mem,net_rx_bps=EXCLUDED.net_rx_bps,net_tx_bps=EXCLUDED.net_tx_bps,kernel=EXCLUDED.kernel,cert_fingerprint=EXCLUDED.cert_fingerprint,ready_protocols=EXCLUDED.ready_protocols`, id.id, v.sequence, v.hash, v.sent, now, v.version, h.SnapshotChecksum, h.CPU, h.Memory, v.rx, v.tx, h.Kernel, id.fingerprint, string(protocolJSON))
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE nodes SET agent_version=$2,online_count=$3,load_score=$4,last_heartbeat_at=$5,updated_at=$5 WHERE id=$1`, id.id, h.AgentVersion, h.OnlineCount, load, now)
	if err != nil {
		return out, err
	}
	if _, err = certificateIdentity(state, s.Roots, s.Clock.Now()); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return Receipt{Accepted: true, Duplicate: false, Sequence: h.Sequence, ReceivedAt: now, ServerTime: now, Actions: []string{}}, nil
}

// loadScore is max(CPU, ceil(online/capacity)). A non-positive capacity does
// not contribute a connection ratio and must not divide by zero.
func loadScore(online, capacity int64, cpu int) int64 {
	var ratio int64
	if capacity > 0 && online > 0 {
		ratio = (online*100 + capacity - 1) / capacity
		if ratio > 100 {
			ratio = 100
		}
	}
	if int64(cpu) > ratio {
		return int64(cpu)
	}
	return ratio
}
