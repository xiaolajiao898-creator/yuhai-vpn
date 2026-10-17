package nodecontrol

import (
	"context"
	"crypto/tls"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 节点流量上报入库与权益扣减（方案 8.5、5.2）。
//
// 扣减顺序按方案 5.2 写死，不是本轮发明的：
//
//	先扣时长套餐周期配额（不限量套餐不扣流量包）→ 再按到期时间从早到晚扣流量包
//
// 全部动作在同一事务内完成：
//   - traffic_reports 以 (node_id, report_id) 为幂等键。重复上报直接返回 duplicate，
//     一个字节都不再累加；
//   - traffic_daily 按 (日期, 设备, 节点) 累加，是账目本身，无论权益是否扣得动都要记；
//   - 权益按用户加锁后扣减，锁顺序按 user_id 升序，避免并发上报互相死锁；
//   - 扣减后若该用户已不可连接，为其全部凭据写 node_user_events(0, 'QUOTA')。
//
// 一处需要产品确认的取舍：**超用部分记在哪里**。方案只说「允许少量超用」，没说去向。
// 这里把超出全部权益的部分记到该用户的时长套餐 period_used_bytes 上（没有套餐则记到
// 最后一个流量包的 used_bytes 上）。两处都只有 >= 0 约束，超出配额正是「已用完」的
// 表示方式，与可连接判定 period_used_bytes < period_quota_bytes 一致。若产品希望超用
// 单独入账，只需改 spillOverage 一处。
type trafficUser struct {
	id    int64
	bytes int64
}

type trafficPack struct {
	id    int64
	total int64
	used  int64
}

func badTrafficRequest() error {
	return httpx.NewError(httpx.ErrBadRequest, "流量上报字段或格式无效")
}

// AcceptTraffic 接收一次节点流量上报。默认关闭（TrafficReportEnabled）。
func (s *Service) AcceptTraffic(ctx context.Context, state *tls.ConnectionState, raw []byte) (nodewire.TrafficReceipt, error) {
	var out nodewire.TrafficReceipt
	if !s.TrafficReportEnabled {
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "流量上报尚未启用")
	}
	now := s.Clock.Now()
	id, err := certificateIdentity(state, s.Roots, now)
	if err != nil {
		return out, err
	}
	report, err := nodewire.ParseTrafficReport(raw, now)
	if err != nil {
		return out, badTrafficRequest()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
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

	// 与心跳同一口径：证书指纹必须与库中当前登记一致，撤销或轮换立即生效。
	var status string
	var cert nodeCert
	if err := tx.QueryRow(ctx, `SELECT status,`+nodeCertColumns+` FROM nodes WHERE id=$1 FOR UPDATE`, id.id).
		Scan(&status, &cert.Current, &cert.Prev, &cert.Until); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, forbidden()
		}
		return out, err
	}
	if status != "ONLINE" || !cert.accepts(id.fingerprint, now) {
		return out, forbidden()
	}

	tag, err := tx.Exec(ctx, `INSERT INTO traffic_reports(node_id,report_id,period_start,period_end,item_count,total_up,total_down,received_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (node_id,report_id) DO NOTHING`,
		id.id, report.ReportID, report.Start, report.End, len(report.Items), report.TotalUp, report.TotalDown, now)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
		// 已处理过：既不重复入账，也不重复扣减权益。
		if err := tx.Commit(ctx); err != nil {
			return out, err
		}
		return nodewire.TrafficReceipt{Accepted: true, Duplicate: true, ItemCount: len(report.Items), ServerTime: now.UTC().Format(time.RFC3339Nano)}, nil
	}

	owners, err := trafficOwners(ctx, tx, report.Credentials)
	if err != nil {
		return out, err
	}
	statDate := report.End.UTC().Format(time.DateOnly)
	perUser := map[int64]int64{}
	for _, item := range report.Items {
		owner, ok := owners[item.CredentialID]
		if !ok {
			// 凭据已被删除或从不属于本系统：无法归属，不入明细也不扣任何人的权益。
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO traffic_daily(stat_date,device_id,node_id,user_id,upload_bytes,download_bytes)
 VALUES($1::date,$2,$3,$4,$5,$6)
 ON CONFLICT (stat_date,device_id,node_id) DO UPDATE
 SET upload_bytes=traffic_daily.upload_bytes+EXCLUDED.upload_bytes,
     download_bytes=traffic_daily.download_bytes+EXCLUDED.download_bytes`,
			statDate, owner.device, id.id, owner.user, item.Up, item.Down); err != nil {
			return out, err
		}
		total := item.Up + item.Down
		if perUser[owner.user] > nodewire.MaxTrafficBytes-total {
			return out, badTrafficRequest()
		}
		perUser[owner.user] += total
	}

	users := make([]trafficUser, 0, len(perUser))
	for uid, bytes := range perUser {
		users = append(users, trafficUser{id: uid, bytes: bytes})
	}
	// 固定加锁顺序，避免两次并发上报涉及同一批用户时互相死锁。
	sort.Slice(users, func(i, j int) bool { return users[i].id < users[j].id })
	for _, u := range users {
		if u.bytes == 0 {
			continue
		}
		if err := s.consume(ctx, tx, u, now); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	return nodewire.TrafficReceipt{Accepted: true, Duplicate: false, ItemCount: len(report.Items), ServerTime: now.UTC().Format(time.RFC3339Nano)}, nil
}

type trafficOwner struct{ device, user int64 }

func trafficOwners(ctx context.Context, tx pgx.Tx, credentials []int64) (map[int64]trafficOwner, error) {
	out := map[int64]trafficOwner{}
	if len(credentials) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT c.id,d.id,d.user_id FROM device_credentials c
 JOIN devices d ON d.id=c.device_id WHERE c.id = ANY($1::bigint[])`, credentials)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var credential int64
		var owner trafficOwner
		if err := rows.Scan(&credential, &owner.device, &owner.user); err != nil {
			return nil, err
		}
		if owner.device <= 0 || owner.user <= 0 {
			return nil, httpx.Internal
		}
		out[credential] = owner
	}
	return out, rows.Err()
}

// consume 扣减单个用户的权益，并在扣减后判定是否已不可连接。
func (s *Service) consume(ctx context.Context, tx pgx.Tx, u trafficUser, now time.Time) error {
	var quota *int64
	var used int64
	var expire *time.Time
	hasSubscription := true
	err := tx.QueryRow(ctx, `SELECT period_quota_bytes,period_used_bytes,expire_at FROM subscriptions WHERE user_id=$1 FOR UPDATE`, u.id).Scan(&quota, &used, &expire)
	if errors.Is(err, pgx.ErrNoRows) {
		hasSubscription = false
	} else if err != nil {
		return err
	}
	subscriptionValid := hasSubscription && expire != nil && expire.After(now)

	rows, err := tx.Query(ctx, `SELECT id,total_bytes,used_bytes FROM traffic_packs
 WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 ORDER BY expire_at,id FOR UPDATE`, u.id, now)
	if err != nil {
		return err
	}
	packs := []trafficPack{}
	for rows.Next() {
		var p trafficPack
		if err := rows.Scan(&p.id, &p.total, &p.used); err != nil {
			rows.Close()
			return err
		}
		packs = append(packs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	remaining := u.bytes
	subscriptionDelta := int64(0)
	if subscriptionValid {
		if quota == nil {
			// 不限量套餐：全部计入周期用量，且按方案不消耗流量包。
			subscriptionDelta, remaining = remaining, 0
		} else if free := *quota - used; free > 0 {
			take := free
			if take > remaining {
				take = remaining
			}
			subscriptionDelta, remaining = take, remaining-take
		}
	}
	packDelta := make([]int64, len(packs))
	for i := range packs {
		if remaining == 0 {
			break
		}
		free := packs[i].total - packs[i].used
		if free <= 0 {
			continue
		}
		take := free
		if take > remaining {
			take = remaining
		}
		packDelta[i], remaining = take, remaining-take
	}
	// 超用：方案允许少量超用但未规定去向，见文件头的取舍说明。
	if remaining > 0 {
		if subscriptionValid {
			subscriptionDelta += remaining
		} else if n := len(packs); n > 0 {
			packDelta[n-1] += remaining
		}
		remaining = 0
	}

	if subscriptionDelta > 0 {
		if _, err := tx.Exec(ctx, `UPDATE subscriptions SET period_used_bytes=period_used_bytes+$2,updated_at=$3 WHERE user_id=$1`, u.id, subscriptionDelta, now); err != nil {
			return err
		}
		used += subscriptionDelta
	}
	for i, delta := range packDelta {
		if delta == 0 {
			continue
		}
		packs[i].used += delta
		// 用满即置为 EXHAUSTED，与到期扫描共同维持 status 的真实性。
		if _, err := tx.Exec(ctx, `UPDATE traffic_packs SET used_bytes=used_bytes+$2,
 status=CASE WHEN used_bytes+$2 >= total_bytes THEN 'EXHAUSTED' ELSE status END WHERE id=$1`, packs[i].id, delta); err != nil {
			return err
		}
	}

	// 方案 5.2 的可连接判定，用扣减后的数值重新计算。
	connectable := subscriptionValid && (quota == nil || used < *quota)
	if !connectable {
		for i := range packs {
			if packs[i].used < packs[i].total {
				connectable = true
				break
			}
		}
	}
	if connectable {
		return nil
	}
	// 只在最新期望状态不是 0 时补事件，保持「设置为目标状态」的幂等语义。
	_, err = tx.Exec(ctx, `INSERT INTO node_user_events(credential_id,effective_tier,reason,created_at)
 SELECT c.id,0,'QUOTA',$2 FROM device_credentials c JOIN devices d ON d.id=c.device_id
  WHERE d.user_id=$1
    AND COALESCE((SELECT e.effective_tier FROM node_user_events e WHERE e.credential_id=c.id ORDER BY e.id DESC LIMIT 1),-1)<>0`, u.id, now)
	return err
}
