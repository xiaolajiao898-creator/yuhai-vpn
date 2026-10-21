package admin

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 总览这一面唯一值得写测试的点，是**它不该给的那些东西**：
//
//   - 只有 users.read 的人不该在首页看到订单数、流量、节点分布；
//   - 看不到的那段必须整块不出现，而不是给 0——0 会被读成「今天没单」；
//   - sections 必须与实际出现的段完全对应，否则前端无法区分「没权限」和「真的是 0」；
//   - 没有权限的那段连表都不该碰（测试里那些表根本没授 SELECT，碰了就会报错）。

type dashboardFixture struct {
	*managedUserFixture
	service *DashboardService
	day     time.Time
}

// newDashboardFixture 建出总览会用到的全部表，但**默认只授 users.read**。
// 各段的表按需在 enable 里授 SELECT：没授的表一旦被查到就会直接报错，
// 这正是「没权限就不该发查询」那条的兜底。
func newDashboardFixture(t *testing.T) *dashboardFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	for _, table := range []string{"node_groups", "nodes", "traffic_daily", "orders",
		"chain_transfers", "deposit_exceptions", "invite_rewards"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
		f.write(t, "REVOKE ALL ON "+pgx.Identifier{f.schema, table}.Sanitize()+" FROM "+
			pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize())
	}
	// 五段的能力定义分散在各自轮次的迁移里：users.read 由基础夹具跑的 00015 定义，
	// 其余四个在这里补上。只跑迁移的 Up 段，不授予任何角色。
	for _, file := range []string{"00017_admin_node_read.sql", "00026_admin_finance_read.sql",
		"00028_admin_invite_review.sql", "00030_admin_traffic_read.sql"} {
		raw, err := os.ReadFile("../db/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		f.write(t, up)
	}
	return &dashboardFixture{managedUserFixture: f, service: &DashboardService{Auth: f.svc.Auth},
		day: f.clock.Now().UTC().Truncate(24 * time.Hour)}
}

// enable 授一段的能力与它需要的表。
func (f *dashboardFixture) enable(t *testing.T, capability string, tables ...string) {
	t.Helper()
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range tables {
		f.write(t, "GRANT SELECT ON "+pgx.Identifier{f.schema, table}.Sanitize()+" TO "+role)
	}
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,$2)`, f.role, capability)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, capability)
	}
}

func TestDashboardOnlyReturnsSectionsTheCallerCanSeeIntegration(t *testing.T) {
	f := newDashboardFixture(t)
	p := f.people[0]

	// 只有 users.read（夹具默认就给了）：只该出现 users 一段。
	out, err := f.service.Get(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sections) != 1 || out.Sections[0] != "users" {
		t.Fatal("段落集合与权限不符", out.Sections)
	}
	if out.Users == nil {
		t.Fatal("users 段缺失", out)
	}
	// 关键：看不到的段必须是 nil（整块不出现），而不是一个装着 0 的结构体。
	if out.Nodes != nil || out.Traffic != nil || out.Finance != nil || out.Invite != nil || out.Site != nil {
		t.Fatal("无权限的段落被填成了 0", out)
	}
	if out.Users.ByStatus["ACTIVE"] != "1" || out.Users.ByStatus["SUSPENDED"] != "1" {
		t.Fatal("用户状态分布", out.Users.ByStatus)
	}
	if out.Users.DevicesActive != "2" {
		t.Fatal("未撤销设备数", out.Users.DevicesActive)
	}
	if _, ok := out.Users.ByStatus["BANNED"]; ok {
		t.Fatal("状态分布不该凭空补出数据库里没有的档位")
	}
}

func TestDashboardAggregatesEachSectionIntegration(t *testing.T) {
	f := newDashboardFixture(t)
	f.enable(t, nodeReadPermission, "nodes")
	f.enable(t, trafficRead, "traffic_daily")
	f.enable(t, financeRead, "orders", "deposit_exceptions")
	f.enable(t, inviteRead, "invite_rewards")
	p := f.people[0]

	f.write(t, `INSERT INTO node_groups(id,name,min_tier) OVERRIDING SYSTEM VALUE
 VALUES(1,'默认',0)`)
	for _, status := range []string{"ONLINE", "ONLINE", "MAINTENANCE"} {
		f.write(t, `INSERT INTO nodes(group_id,name,country_code,city,region,host,status)
 VALUES(1,'n','HK','HK','ASIA','h.example.com',$1)`, status)
	}
	// 窗口内两天 + 窗口外一天（8 天前，默认窗口是 7 天含今天）。
	for _, row := range []struct {
		offset   int
		up, down int64
	}{{0, 10, 20}, {-6, 1, 2}, {-7, 500, 500}} {
		f.write(t, `INSERT INTO traffic_daily(stat_date,device_id,node_id,user_id,upload_bytes,download_bytes)
 VALUES($1::date,$2,1,$3,$4,$5)`, f.day.AddDate(0, 0, row.offset).Format(time.DateOnly),
			int64(100+row.offset*-1), f.userID, row.up, row.down)
	}
	// 今天两单 + 昨天一单：只数今天的。
	for i, at := range []time.Time{f.day, f.day.Add(23 * time.Hour), f.day.Add(-time.Hour)} {
		f.write(t, `INSERT INTO orders(order_no,user_id,order_type,base_amount_micro,pay_amount_micro,
 pay_method,receive_address_id,status,client_idem_key,expires_at,created_at,updated_at)
 VALUES($1,$2,'TOPUP',1,1,'USDT_TRC20',1,'PENDING',$3,$4,$4,$4)`,
			"DASH-ORDER-"+string(rune('A'+i)), f.userID, "dash-key-"+string(rune('A'+i)),
			at.Add(time.Minute))
	}
	// 到账异常挂在链上转账上（外键在测试 schema 里也被 LIKE INCLUDING ALL 带了过来）。
	f.write(t, `INSERT INTO chain_transfers(id,network,tx_hash,contract_address,from_address,to_address,
 receive_address_id,amount_micro,block_number,block_time,confirm_status,match_status,data_source,created_at)
 OVERRIDING SYSTEM VALUE
 VALUES(1,'MOCK','0xabc','contract','from','to',1,1,1,$1,'SOLIDIFIED','EXCEPTION','TEST',$1)`,
		f.clock.Now())
	f.write(t, `INSERT INTO deposit_exceptions(chain_transfer_id,reason,status,created_at)
 VALUES(1,'NO_MATCHING_ORDER','OPEN',$1)`, f.clock.Now())
	// 三档状态各一条：唯一约束按 (rule_id, beneficiary, invitee, trigger_ref) 去重，
	// 因此每条换一个 trigger_ref。
	for i, status := range []string{"REVIEW", "PENDING", "RELEASED"} {
		f.write(t, `INSERT INTO invite_rewards(rule_id,rule_snapshot,inviter_user_id,invitee_user_id,
 beneficiary_user_id,trigger_ref,reward_type,reward_value,status,release_at,created_at)
 VALUES(1,'{}',$1,$1,$1,$2,'BALANCE',1,$3,$4,$4)`, f.userID,
			"ref-"+string(rune('A'+i)), status, f.clock.Now())
	}

	out, err := f.service.Get(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Sections, ",") != "users,nodes,traffic,finance,invite" {
		t.Fatal("段落顺序或集合不对", out.Sections)
	}
	if out.Nodes.ByStatus["ONLINE"] != "2" || out.Nodes.ByStatus["MAINTENANCE"] != "1" {
		t.Fatal("节点状态分布", out.Nodes.ByStatus)
	}
	if out.Traffic.WindowDays != 7 || out.Traffic.To != f.day.Format(time.DateOnly) ||
		out.Traffic.From != f.day.AddDate(0, 0, -6).Format(time.DateOnly) {
		t.Fatal("流量窗口", out.Traffic)
	}
	// 窗口外那天（500+500）必须被挡在外面。
	if out.Traffic.UploadBytes != "11" || out.Traffic.DownloadBytes != "22" ||
		out.Traffic.TotalBytes != "33" {
		t.Fatal("流量窗口外的数据混了进来", out.Traffic)
	}
	if out.Finance.OrdersCreatedToday != "2" {
		t.Fatal("今日订单数按 UTC 自然日切", out.Finance)
	}
	if out.Finance.DepositExceptionsOpen != "1" {
		t.Fatal("待处理到账异常", out.Finance)
	}
	if out.Invite.RewardsInReview != "1" {
		t.Fatal("只数 REVIEW，PENDING 归 worker", out.Invite)
	}
}

func TestDashboardEmptyIsZeroNotMissingIntegration(t *testing.T) {
	f := newDashboardFixture(t)
	f.enable(t, trafficRead, "traffic_daily")
	out, err := f.service.Get(f.ctx, f.people[0])
	if err != nil {
		t.Fatal(err)
	}
	// 有权限但没有数据：这一段要出现，且是 0。与「没权限所以不出现」是两回事。
	if out.Traffic == nil || out.Traffic.TotalBytes != "0" {
		t.Fatal("有权限的空数据段应当出现并为 0", out.Traffic)
	}
	if len(out.Sections) != 2 {
		t.Fatal("段落集合", out.Sections)
	}
}

func TestDashboardWithoutAnyCapabilityIsForbiddenIntegration(t *testing.T) {
	f := newDashboardFixture(t)
	p := f.people[0]
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='users.read'`, f.role)
	// 一段都看不到时返回 403，而不是一个空壳——空壳会被读成「系统里什么都没有」。
	_, err := f.service.Get(f.ctx, p)
	nodeCode(t, err, httpx.ErrForbidden)
}
