package nodecontrol

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/clock"
)

// 撤销执行回执对账。
//
// 在此之前，管理员撤销设备只能得到 node_sync=QUEUED，即「期望状态已排队」——那不是
// 节点回执，不能对外说成「已在节点生效」。本文件补上缺失的确认环节。
//
// 判定依据只有一条：节点自己上报的 snapshot_checksum，与控制面此刻重算出的该节点公开
// 清单校验和完全一致。校验和是清单内 peer 集合的 SHA-256，所以相等即意味着节点运行中的
// peer 集合与控制面的期望集合逐条一致。在此前提下，凡是期望被移除、且确实不在该清单中的
// 凭据，就可以判定为「该节点上已不存在」。
//
// 刻意不做的事：
//   - 不因为排队事件存在、或过了多久，就认为撤销已生效；
//   - 不因为节点心跳只是「到达」就认为已收敛——校验和不符一律跳过；
//   - 不采信陈旧心跳（默认 90 秒，与 App 侧节点候选门槛同一口径）；
//   - 不采信空校验和（该节点没有在跑 WireGuard，无从谈起）；
//   - 计算清单失败时该节点整体跳过，绝不写入半截结论。
type RemovalReceipts struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
	// 默认关闭。关闭时 ConfirmOnce 不读不写，直接返回。
	Enabled bool
	// 心跳新鲜度窗口，零值按 90 秒。
	Freshness time.Duration
}

type convergedNode struct {
	id       int64
	tier     int
	sequence int64
	checksum string
}

// ConfirmOnce 扫描一轮所有心跳新鲜的节点，为已收敛节点写入回执。返回本轮新增的回执条数。
// 可重入：同一 (node_id, credential_id) 只会留下第一次确认的时间。
func (r *RemovalReceipts) ConfirmOnce(ctx context.Context) (int, error) {
	if !r.Enabled || r.Pool == nil || r.Clock == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := r.Clock.Now()
	window := r.Freshness
	if window <= 0 {
		window = 90 * time.Second
	}

	nodes, err := r.freshNodes(ctx, now, window)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, n := range nodes {
		count, err := r.confirmNode(ctx, n, now)
		if err != nil {
			// 单个节点失败不影响其余节点；本轮结果如实返回错误。
			return written, err
		}
		written += count
	}
	return written, nil
}

func (r *RemovalReceipts) freshNodes(ctx context.Context, now time.Time, window time.Duration) ([]convergedNode, error) {
	rows, err := r.Pool.Query(ctx, `
 SELECT n.id,g.min_tier,h.sequence,h.snapshot_checksum
   FROM nodes n
   JOIN node_groups g ON g.id=n.group_id
   JOIN node_heartbeats h ON h.node_id=n.id
  WHERE h.snapshot_checksum <> ''
    AND h.received_at > $1 AND h.received_at <= $2
  ORDER BY n.id LIMIT 200`, now.Add(-window), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []convergedNode
	for rows.Next() {
		var n convergedNode
		if err := rows.Scan(&n.id, &n.tier, &n.sequence, &n.checksum); err != nil {
			return nil, err
		}
		if n.tier < 1 || n.tier > 9 || n.sequence <= 0 {
			continue
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// confirmNode 只在该节点确实收敛到当前清单时写入回执。
func (r *RemovalReceipts) confirmNode(ctx context.Context, n convergedNode, now time.Time) (int, error) {
	present, digest, err := r.desiredPeers(ctx, n, now)
	if err != nil {
		// 清单算不出来（节点未配置 WireGuard 入站等）不是错误，跳过该节点。
		return 0, nil
	}
	if digest != n.checksum {
		return 0, nil // 节点尚未收敛到当前期望状态
	}
	tag, err := r.Pool.Exec(ctx, `
 INSERT INTO node_credential_receipts(node_id,credential_id,confirmed_absent_at,heartbeat_sequence,snapshot_checksum)
 SELECT $1,c.id,$2,$3,$4 FROM device_credentials c
  WHERE COALESCE((SELECT e.effective_tier FROM node_user_events e
                   WHERE e.credential_id=c.id ORDER BY e.id DESC LIMIT 1),-1)=0
    AND NOT (c.id = ANY($5::bigint[]))
 ON CONFLICT (node_id,credential_id) DO NOTHING`, n.id, now, n.sequence, n.checksum, present)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// desiredPeers 在只读快照内重算该节点的清单，返回其中的凭据 ID 与校验和。
func (r *RemovalReceipts) desiredPeers(ctx context.Context, n convergedNode, now time.Time) ([]int64, string, error) {
	tx, err := r.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, "", err
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(c)
	}()
	issued := now.UTC().Truncate(time.Microsecond)
	manifest, digest, _, err := wgDesiredState(ctx, tx, n.id, n.tier, issued, issued.Add(30*time.Second))
	if err != nil {
		return nil, "", err
	}
	present := make([]int64, 0, len(manifest.Peers))
	for _, p := range manifest.Peers {
		id, err := wgCredentialID(p.CredentialID)
		if err != nil {
			return nil, "", err
		}
		present = append(present, id)
	}
	return present, digest, tx.Commit(ctx)
}

// wgCredentialID 把清单里的字符串凭据 ID 解回整数；清单已通过严格校验，此处只做防御。
func wgCredentialID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != s {
		return 0, errors.New("凭据标识无效")
	}
	return n, nil
}
