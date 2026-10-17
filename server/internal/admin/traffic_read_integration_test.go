package admin

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 流量统计这一面要钉住的，是它和「把 traffic_daily 直接 SELECT 出来」不一样的那几件事：
//
//   - **字节数必须是字符串，且不丢精度**：单个分组的合计可以超过 int64，
//     渲染成 JSON 数字就会被前端读成浮点，账面上那是错的。
//   - **窗口是闭区间，边界日期要在里面**，窗口外的行一个字节都不许混进来。
//   - **不自作主张纠正参数**：from 晚于 to 不交换、跨度超限不截断、不合法的日期不忽略。
//   - **分页必须有全序**：合计并列时靠维度键兜底，否则翻页会重复给同一行。
//   - **权限是独立的**：只有 users.read 看不到流量。

type trafficFixture struct {
	*managedUserFixture
	service *TrafficReadService
	day     time.Time
}

func newTrafficFixture(t *testing.T) *trafficFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	name := pgx.Identifier{f.schema, "traffic_daily"}.Sanitize()
	// public.traffic_daily 是分区表，LIKE 出来的是普通表——统计只读，分区与否不影响被测逻辑。
	f.write(t, "CREATE TABLE "+name+" (LIKE "+pgx.Identifier{"public", "traffic_daily"}.Sanitize()+
		" INCLUDING ALL)")
	// 只给 SELECT：这个接口永远不写流量表，权限上就不该有写的余地。
	f.write(t, "REVOKE ALL ON "+name+" FROM "+role+"; GRANT SELECT ON "+name+" TO "+role)

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00030_admin_traffic_read.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='traffic.read' AND risk_level=0`) != 1 {
		t.Fatal("traffic.read 能力未定义或风险等级不对")
	}

	out := &trafficFixture{managedUserFixture: f, service: &TrafficReadService{Auth: f.svc.Auth},
		day: f.clock.Now().UTC().Truncate(24 * time.Hour)}
	return out
}

// grant 把 traffic.read 授给测试角色。默认不授——「只有 users.read 看不到流量」那条用例要靠它。
func (f *trafficFixture) grant(t *testing.T) {
	t.Helper()
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'traffic.read')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, trafficRead)
	}
}

func (f *trafficFixture) row(t *testing.T, dayOffset int, device, node, user, up, down int64) {
	t.Helper()
	f.write(t, `INSERT INTO traffic_daily(stat_date,device_id,node_id,user_id,upload_bytes,download_bytes)
 VALUES($1::date,$2,$3,$4,$5,$6)`, f.day.AddDate(0, 0, dayOffset).Format(time.DateOnly),
		device, node, user, up, down)
}

func TestTrafficStatsAggregatesWindowAndPrecisionIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	f.grant(t)
	p := f.people[0]

	// 今天：两台设备、两个节点、两个用户；合计刻意用 int64 上限附近的数字，
	// 逼出「超过 2^53 的合计必须原样渲染成字符串」这条。
	f.row(t, 0, 11, 21, 101, math.MaxInt64, 0)
	f.row(t, 0, 12, 22, 102, 1, 2)
	// 昨天：同一台设备再来一笔，用来验证按设备/用户聚合会跨天相加。
	f.row(t, -1, 11, 21, 101, 3, 4)
	// 窗口外：31 天前（默认窗口是最近 30 天含今天），一个字节都不许进来。
	f.row(t, -30, 13, 23, 103, 999, 999)

	day, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if day.Dim != "day" || day.To != f.day.Format(time.DateOnly) ||
		day.From != f.day.AddDate(0, 0, -29).Format(time.DateOnly) {
		t.Fatal("默认窗口不是最近 30 天", day.From, day.To)
	}
	if day.Total != "2" || len(day.Items) != 2 {
		t.Fatal("按日分组数量不对", day.Total, len(day.Items))
	}
	if day.Items[0].StatDate == nil || *day.Items[0].StatDate != f.day.Format(time.DateOnly) {
		t.Fatal("按日未按日期倒序", day.Items[0])
	}
	if day.Items[0].UploadBytes != "9223372036854775808" || day.Items[0].DownloadBytes != "2" ||
		day.Items[0].TotalBytes != "9223372036854775810" {
		t.Fatal("合计精度丢失（超过 int64 必须原样给出）", day.Items[0])
	}
	if day.Items[0].UserID != nil || day.Items[0].DeviceID != nil || day.Items[0].NodeID != nil {
		t.Fatal("按日维度混进了别的维度键", day.Items[0])
	}
	if day.Items[1].TotalBytes != "7" {
		t.Fatal("昨天合计不对", day.Items[1])
	}

	user, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "user"})
	if err != nil || user.Total != "2" || len(user.Items) != 2 {
		t.Fatal("按用户分组", user, err)
	}
	if user.Items[0].UserID == nil || *user.Items[0].UserID != "101" ||
		user.Items[0].TotalBytes != "9223372036854775814" {
		t.Fatal("按用户未跨天相加或未按合计倒序", user.Items[0])
	}
	if user.Items[0].StatDate != nil || user.Items[0].DeviceID != nil {
		t.Fatal("按用户维度混进了别的维度键", user.Items[0])
	}

	device, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "device"})
	if err != nil || len(device.Items) != 2 {
		t.Fatal("按设备分组", device, err)
	}
	// 设备维度额外带出所属用户：一台设备只属于一个用户，不会因此被拆成两行。
	if device.Items[0].DeviceID == nil || *device.Items[0].DeviceID != "11" ||
		device.Items[0].UserID == nil || *device.Items[0].UserID != "101" ||
		device.Items[0].TotalBytes != "9223372036854775814" {
		t.Fatal("按设备聚合", device.Items[0])
	}

	node, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "node"})
	if err != nil || len(node.Items) != 2 {
		t.Fatal("按节点分组", node, err)
	}
	if node.Items[0].NodeID == nil || *node.Items[0].NodeID != "21" || node.Items[0].UserID != nil {
		t.Fatal("按节点维度不应带出用户", node.Items[0])
	}

	// 显式窗口：闭区间，边界那天必须在里面。
	edge, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day",
		From: f.day.AddDate(0, 0, -1).Format(time.DateOnly), To: f.day.AddDate(0, 0, -1).Format(time.DateOnly)})
	if err != nil || edge.Total != "1" || len(edge.Items) != 1 || edge.Items[0].TotalBytes != "7" {
		t.Fatal("闭区间边界", edge, err)
	}
	// 把窗口拉到 31 天前那笔，证明它确实存在——前面看不到它是窗口起的作用，不是数据没写进去。
	old, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day",
		From: f.day.AddDate(0, 0, -30).Format(time.DateOnly), To: f.day.AddDate(0, 0, -30).Format(time.DateOnly)})
	if err != nil || old.Total != "1" || old.Items[0].TotalBytes != "1998" {
		t.Fatal("窗口外那笔应当存在", old, err)
	}
}

func TestTrafficStatsRejectsBadParametersIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	f.grant(t)
	p := f.people[0]
	today := f.day.Format(time.DateOnly)

	for name, f2 := range map[string]TrafficFilter{
		"维度缺失":   {},
		"维度非法":   {Dim: "hour"},
		"维度大小写":  {Dim: "DAY"},
		"日期位数不足": {Dim: "day", From: "2026-9-1", To: today},
		"日历上不存在": {Dim: "day", From: "2026-02-31", To: today},
		"起晚于止":   {Dim: "day", From: today, To: f.day.AddDate(0, 0, -1).Format(time.DateOnly)},
		"区间过长":   {Dim: "day", From: f.day.AddDate(0, 0, -366).Format(time.DateOnly), To: today},
		"页码越界":   {Dim: "day", Page: 10001},
		"每页过大":   {Dim: "day", PageSize: 51},
	} {
		_, err := f.service.Stats(f.ctx, p, f2)
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 刚好 366 天（含两端）必须放行——上限是「超过才拒」，不是「等于就拒」。
	if _, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day",
		From: f.day.AddDate(0, 0, -365).Format(time.DateOnly), To: today}); err != nil {
		t.Fatal("366 天整应当放行", err)
	}
}

func TestTrafficStatsRequiresOwnCapabilityIntegration(t *testing.T) {
	f := newTrafficFixture(t) // 刻意不授 traffic.read
	p := f.people[0]
	// 夹具已经授了 users.read / users.ledgers.read，这里仍应 403：
	// 能看用户资料不等于能看全站流量分布。
	_, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day"})
	nodeCode(t, err, httpx.ErrForbidden)
	f.grant(t)
	if _, err = f.service.Stats(f.ctx, p, TrafficFilter{Dim: "day"}); err != nil {
		t.Fatal("授权后仍不可读", err)
	}
}

func TestTrafficStatsPaginationIsTotallyOrderedIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	f.grant(t)
	p := f.people[0]
	// 三个节点合计完全相同：没有兜底排序时，两页之间可以合法地重复给同一行。
	for i, node := range []int64{31, 32, 33} {
		f.row(t, 0, int64(40+i), node, 201, 5, 5)
	}
	seen := map[string]bool{}
	for page := 1; page <= 3; page++ {
		out, err := f.service.Stats(f.ctx, p, TrafficFilter{Dim: "node", Page: page, PageSize: 1})
		if err != nil || out.Total != "3" || len(out.Items) != 1 {
			t.Fatal("分页", page, out, err)
		}
		id := *out.Items[0].NodeID
		if seen[id] {
			t.Fatal("翻页重复返回同一节点", id)
		}
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Fatal("三页没有覆盖全部节点", seen)
	}
}

func TestTrafficStatsEmptyWindowIsNotNullIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	f.grant(t)
	out, err := f.service.Stats(f.ctx, f.people[0], TrafficFilter{Dim: "user"})
	if err != nil || out.Total != "0" {
		t.Fatal("空窗口", out, err)
	}
	// items 必须是空数组而不是 null：前端拿到 null 会崩在 .map 上。
	if out.Items == nil || len(out.Items) != 0 {
		t.Fatal("空结果应当是空数组", out.Items)
	}
	if out.ObservedAt.IsZero() {
		t.Fatal("缺少快照时间")
	}
}
