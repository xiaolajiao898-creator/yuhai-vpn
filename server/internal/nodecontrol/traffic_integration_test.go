package nodecontrol

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 流量上报直接决定用户的权益还剩多少，因此这里盯的是三件事：
// 同一份上报绝不被算两次、扣减顺序与方案 5.2 一致、扣到不可连接时才写 QUOTA 事件。

func newTrafficFixture(t *testing.T) *heartbeatFixture {
	t.Helper()
	f := newWGFixture(t)
	f.svc.TrafficReportEnabled = true
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()

	// traffic_daily 必须用真实的分区 DDL 建，否则测不到「有没有分区」这件事。
	f.exec(t, `CREATE TABLE traffic_daily (
  stat_date       DATE    NOT NULL,
  device_id       BIGINT  NOT NULL,
  node_id         BIGINT  NOT NULL,
  user_id         BIGINT  NOT NULL,
  upload_bytes    BIGINT  NOT NULL DEFAULT 0,
  download_bytes  BIGINT  NOT NULL DEFAULT 0,
  PRIMARY KEY (stat_date, device_id, node_id)
) PARTITION BY RANGE (stat_date)`)
	f.exec(t, "CREATE TABLE "+pgx.Identifier{"traffic_reports"}.Sanitize()+
		" (LIKE "+pgx.Identifier{"public", "traffic_reports"}.Sanitize()+" INCLUDING ALL)")
	f.exec(t, "CREATE TABLE "+pgx.Identifier{"node_user_events"}.Sanitize()+
		" (LIKE "+pgx.Identifier{"public", "node_user_events"}.Sanitize()+" INCLUDING ALL)")

	// 用真实迁移创建默认分区并授权。
	raw, err := os.ReadFile("../db/migrations/00022_traffic_ingest.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err := f.owner.Exec(f.ctx, up); err != nil {
		t.Fatal("实际迁移执行失败", err)
	}
	for _, sql := range []string{
		"GRANT SELECT,INSERT,UPDATE ON traffic_daily TO " + role,
		"GRANT SELECT,INSERT ON traffic_reports TO " + role,
		"GRANT SELECT,INSERT ON node_user_events TO " + role,
		"GRANT UPDATE(period_used_bytes,updated_at) ON subscriptions TO " + role,
		"GRANT SELECT(id),UPDATE(used_bytes,status) ON traffic_packs TO " + role,
	} {
		f.exec(t, sql)
	}
	return f
}

func trafficItems(items ...[3]int64) string {
	parts := make([]string, 0, len(items))
	for _, i := range items {
		parts = append(parts, fmt.Sprintf(`{"credential_id":"%d","up_bytes":"%d","down_bytes":"%d"}`, i[0], i[1], i[2]))
	}
	return strings.Join(parts, ",")
}

func report(t *testing.T, f *heartbeatFixture, reportID string, items string) (accepted, duplicate bool) {
	t.Helper()
	now := f.clock.Now()
	body := fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":[%s]}`,
		reportID, now.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05Z"), now.UTC().Format("2006-01-02T15:04:05Z"), items)
	out, err := f.svc.AcceptTraffic(f.ctx, f.ca.state(f.cert), []byte(body))
	if err != nil {
		t.Fatalf("上报失败：%v", err)
	}
	return out.Accepted, out.Duplicate
}

// subscription 为用户写一条时长套餐。quota 为 nil 表示不限量。
func subscription(t *testing.T, f *heartbeatFixture, userID int64, quota *int64, used int64, expire time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,device_limit,expire_at,period_quota_bytes,period_used_bytes)
 VALUES($1,1,3,$2,$3,$4) ON CONFLICT(user_id) DO UPDATE SET period_quota_bytes=EXCLUDED.period_quota_bytes,
 period_used_bytes=EXCLUDED.period_used_bytes,expire_at=EXCLUDED.expire_at`, userID, expire, quota, used)
}

func pack(t *testing.T, f *heartbeatFixture, id, userID, total, used int64, expire time.Time) {
	t.Helper()
	f.exec(t, `INSERT INTO traffic_packs(id,user_id,tier,device_limit,total_bytes,used_bytes,expire_at,source_type,source_id,status)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,1,3,$3,$4,$5,'ORDER',$1,'ACTIVE')`, id, userID, total, used, expire)
}

func subUsed(t *testing.T, f *heartbeatFixture, userID int64) int64 {
	t.Helper()
	var n int64
	if err := f.owner.QueryRow(f.ctx, `SELECT period_used_bytes FROM subscriptions WHERE user_id=$1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func packState(t *testing.T, f *heartbeatFixture, id int64) (int64, string) {
	t.Helper()
	var used int64
	var status string
	if err := f.owner.QueryRow(f.ctx, `SELECT used_bytes,status FROM traffic_packs WHERE id=$1`, id).Scan(&used, &status); err != nil {
		t.Fatal(err)
	}
	return used, status
}

func dailyTotals(t *testing.T, f *heartbeatFixture, deviceID int64) (int64, int64) {
	t.Helper()
	var up, down int64
	err := f.owner.QueryRow(f.ctx, `SELECT COALESCE(SUM(upload_bytes),0),COALESCE(SUM(download_bytes),0) FROM traffic_daily WHERE device_id=$1`, deviceID).Scan(&up, &down)
	if err != nil {
		t.Fatal(err)
	}
	return up, down
}

func quotaEvents(t *testing.T, f *heartbeatFixture) int {
	t.Helper()
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM node_user_events WHERE reason='QUOTA'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const reportA = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
const reportB = "3f2504e0-4f89-41d3-9a0c-0305e82c3302"

func TestTrafficReportIsIdempotentIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	quota := int64(1000)
	subscription(t, f, 11, &quota, 0, f.clock.Now().Add(time.Hour))

	if accepted, duplicate := report(t, f, reportA, trafficItems([3]int64{11, 100, 200})); !accepted || duplicate {
		t.Fatal("首次上报应当被接受且非重复")
	}
	up, down := dailyTotals(t, f, 11)
	if up != 100 || down != 200 || subUsed(t, f, 11) != 300 {
		t.Fatalf("首次入账不符：up=%d down=%d used=%d", up, down, subUsed(t, f, 11))
	}

	// 同一 report_id 再报：必须原样返回重复，且一个字节都不再累加。
	if accepted, duplicate := report(t, f, reportA, trafficItems([3]int64{11, 100, 200})); !accepted || !duplicate {
		t.Fatal("重复上报应当返回 duplicate")
	}
	up, down = dailyTotals(t, f, 11)
	if up != 100 || down != 200 || subUsed(t, f, 11) != 300 {
		t.Fatalf("重复上报被重复计费：up=%d down=%d used=%d", up, down, subUsed(t, f, 11))
	}

	// 换一个 report_id 才继续累加。
	if _, duplicate := report(t, f, reportB, trafficItems([3]int64{11, 1, 2})); duplicate {
		t.Fatal("不同 report_id 被当作重复")
	}
	if up, down = dailyTotals(t, f, 11); up != 101 || down != 202 || subUsed(t, f, 11) != 303 {
		t.Fatalf("后续上报未累加：up=%d down=%d used=%d", up, down, subUsed(t, f, 11))
	}
}

func TestTrafficConsumesSubscriptionBeforePacksIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	now := f.clock.Now()
	quota := int64(500)
	subscription(t, f, 11, &quota, 300, now.Add(time.Hour))
	// 两个流量包，按到期时间从早到晚扣。
	pack(t, f, 1, 11, 1000, 0, now.Add(2*time.Hour))
	pack(t, f, 2, 11, 1000, 0, now.Add(48*time.Hour))

	// 上报 700：套餐剩余 200 → 扣满；余 500 落到最早到期的流量包。
	report(t, f, reportA, trafficItems([3]int64{11, 700, 0}))

	if used := subUsed(t, f, 11); used != 500 {
		t.Fatalf("周期配额应当被扣满到 500，实际 %d", used)
	}
	if used, status := packState(t, f, 1); used != 500 || status != "ACTIVE" {
		t.Fatalf("最早到期的流量包扣减不符：used=%d status=%s", used, status)
	}
	if used, _ := packState(t, f, 2); used != 0 {
		t.Fatalf("较晚到期的流量包不应被动用，实际 %d", used)
	}
	if quotaEvents(t, f) != 0 {
		t.Fatal("仍有余量却写了 QUOTA 事件")
	}
}

func TestTrafficUnlimitedSubscriptionNeverTouchesPacksIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	now := f.clock.Now()
	// 方案 5.2：不限量套餐不扣流量包。
	subscription(t, f, 11, nil, 0, now.Add(time.Hour))
	pack(t, f, 1, 11, 1000, 0, now.Add(2*time.Hour))

	report(t, f, reportA, trafficItems([3]int64{11, 5000, 5000}))

	if used := subUsed(t, f, 11); used != 10000 {
		t.Fatalf("不限量套餐也应记录周期用量，实际 %d", used)
	}
	if used, status := packState(t, f, 1); used != 0 || status != "ACTIVE" {
		t.Fatalf("不限量套餐不得消耗流量包：used=%d status=%s", used, status)
	}
	if quotaEvents(t, f) != 0 {
		t.Fatal("不限量套餐被判定为不可连接")
	}
}

func TestTrafficExhaustionWritesQuotaEventOnceIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	now := f.clock.Now()
	quota := int64(100)
	subscription(t, f, 11, &quota, 0, now.Add(time.Hour))
	pack(t, f, 1, 11, 200, 0, now.Add(2*time.Hour))

	// 用满配额与流量包，并多出 50 字节的超用。
	report(t, f, reportA, trafficItems([3]int64{11, 350, 0}))

	if used, status := packState(t, f, 1); used != 200 || status != "EXHAUSTED" {
		t.Fatalf("流量包应当用尽并置为 EXHAUSTED：used=%d status=%s", used, status)
	}
	// 超用记在周期用量上：100 配额 + 50 超用。
	if used := subUsed(t, f, 11); used != 150 {
		t.Fatalf("超用未按约定记账，实际 %d", used)
	}
	if n := quotaEvents(t, f); n != 1 {
		t.Fatalf("用尽后应当写 1 条 QUOTA 事件，实际 %d", n)
	}
	// 再次上报不得重复写事件：期望状态已经是 0。
	report(t, f, reportB, trafficItems([3]int64{11, 10, 0}))
	if n := quotaEvents(t, f); n != 1 {
		t.Fatalf("重复写入了 QUOTA 事件，实际 %d", n)
	}
}

func TestTrafficRecordsUsageWithoutEntitlementIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	// 没有任何套餐或流量包：明细仍然是账，必须照记；同时应当判定为不可连接。
	f.exec(t, `DELETE FROM subscriptions WHERE user_id=$1`, int64(11))

	report(t, f, reportA, trafficItems([3]int64{11, 7, 8}))

	if up, down := dailyTotals(t, f, 11); up != 7 || down != 8 {
		t.Fatalf("无权益时明细未记账：up=%d down=%d", up, down)
	}
	if n := quotaEvents(t, f); n != 1 {
		t.Fatalf("无权益应当写 QUOTA 事件，实际 %d", n)
	}
}

func TestTrafficSkipsUnknownCredentialsIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	quota := int64(1000)
	subscription(t, f, 11, &quota, 0, f.clock.Now().Add(time.Hour))

	// 999 不存在：无法归属，既不入明细也不扣任何人的权益，但整份上报照常接收。
	report(t, f, reportA, trafficItems([3]int64{11, 10, 0}, [3]int64{999, 5000, 5000}))

	if up, down := dailyTotals(t, f, 11); up != 10 || down != 0 {
		t.Fatalf("已知凭据入账不符：up=%d down=%d", up, down)
	}
	if used := subUsed(t, f, 11); used != 10 {
		t.Fatalf("未知凭据的流量被算到了别人头上，实际 %d", used)
	}
	var rows int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM traffic_daily`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("未知凭据产生了明细行，共 %d 行", rows)
	}
}

func TestTrafficSeparatesUsersIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	addWGUser(t, f, 12)
	now := f.clock.Now()
	quota := int64(1000)
	subscription(t, f, 11, &quota, 0, now.Add(time.Hour))
	subscription(t, f, 12, &quota, 0, now.Add(time.Hour))

	report(t, f, reportA, trafficItems([3]int64{11, 100, 0}, [3]int64{12, 7, 0}))

	if used := subUsed(t, f, 11); used != 100 {
		t.Fatalf("用户 11 扣减不符，实际 %d", used)
	}
	if used := subUsed(t, f, 12); used != 7 {
		t.Fatalf("用户 12 扣减不符，实际 %d", used)
	}
}

func TestTrafficRejectsUntrustedOrDisabledIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	body := fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":[]}`,
		reportA, f.clock.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05Z"), f.clock.Now().UTC().Format("2006-01-02T15:04:05Z"))

	// 开关关闭时接口不可用。
	f.svc.TrafficReportEnabled = false
	if _, err := f.svc.AcceptTraffic(f.ctx, f.ca.state(f.cert), []byte(body)); err == nil {
		t.Fatal("开关关闭时仍然接收了上报")
	}
	f.svc.TrafficReportEnabled = true

	// 节点不在线时拒绝，避免离线/维护中的节点继续驱动计费。
	f.exec(t, `UPDATE nodes SET status='MAINTENANCE'`)
	_, err := f.svc.AcceptTraffic(f.ctx, f.ca.state(f.cert), []byte(body))
	if err == nil {
		t.Fatal("非在线节点的上报被接收")
	}
	f.exec(t, `UPDATE nodes SET status='ONLINE'`)

	// 证书指纹与库中登记不符时拒绝。
	f.exec(t, `UPDATE nodes SET agent_cert_fingerprint=$1`, strings.Repeat("f", 64))
	if _, err := f.svc.AcceptTraffic(f.ctx, f.ca.state(f.cert), []byte(body)); err == nil {
		t.Fatal("指纹不符的上报被接收")
	}
}

func TestTrafficRejectsMalformedBodyIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	now := f.clock.Now()
	bad := fmt.Sprintf(`{"report_id":"not-a-uuid","window_start":%q,"window_end":%q,"items":[]}`,
		now.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05Z"), now.UTC().Format("2006-01-02T15:04:05Z"))
	_, err := f.svc.AcceptTraffic(f.ctx, f.ca.state(f.cert), []byte(bad))
	var coded *httpx.Error
	if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
		t.Fatalf("格式错误应当返回 400，实际 %v", err)
	}
	// 被拒绝的请求不得留下任何痕迹。
	var reports int
	if e := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM traffic_reports`).Scan(&reports); e != nil {
		t.Fatal(e)
	}
	if reports != 0 {
		t.Fatal("非法上报仍写入了 traffic_reports")
	}
}
