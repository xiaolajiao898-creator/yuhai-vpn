package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/plan"
)

// 套餐这组接口真正要钉住的只有一条：**实质改动必须让 version + 1，外观改动不许动它。**
//
// 光断言「版本号加了 1」是不够的——那只是个整数。本文件里有一个用例直接拿
// internal/plan 的 LockQuote 来验证后果：改价之后，一张还没用的报价**真的**下不了单了；
// 而只改角标时，同一张报价照样有效。这才是这条规矩存在的理由。

type planFixture struct {
	*managedUserFixture
	read   *PlanReadService
	writer *PlanWriteService
}

func newPlanFixture(t *testing.T) *planFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"plans", "plan_quotes"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00029_admin_plan_write.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code IN('plans.create','plans.update','plans.status') AND risk_level=2`) != 3 {
		t.Fatal("套餐写能力未定义或风险等级不对")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='plans.read' AND risk_level=0`) != 1 {
		t.Fatal("plans.read 能力未定义")
	}

	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{"plans"}.Sanitize()+" TO "+role)
	// plan_quotes 的 UPDATE 是 LockQuote 的 FOR UPDATE OF q 需要的，本测试自己要用到它；
	// admin-api 本身不碰这张表。
	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{"plan_quotes"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{"admin_plan_mutations"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'plans.read'),($1,'plans.create'),($1,'plans.update'),($1,'plans.status')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, planRead,
			actionPlanCreate, actionPlanUpdate, actionPlanStatus)
	}
	return &planFixture{managedUserFixture: f, read: &PlanReadService{Auth: f.svc.Auth},
		writer: &PlanWriteService{Security: f.svc}}
}

func (f *planFixture) planProof(t *testing.T, action, method, path string, body any) ApprovalProof {
	t.Helper()
	raw, _ := json.Marshal(body)
	p := f.proof(t, 0, action, path, string(raw))
	p.RequestHash = RequestHash(method, path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func timePlan(code string) PlanCreate {
	quota, days, devices := "214748364800", 30, 3
	return PlanCreate{OperationID: uuid.NewString(), Reason: "上新套餐", Code: code,
		Name: "标准版 1 个月", PlanType: "TIME", Tier: 1, Price: "9.900000",
		DurationDays: &days, PeriodQuotaBytes: &quota, DeviceLimit: &devices, SortOrder: 10}
}

func (f *planFixture) create(t *testing.T, in PlanCreate) ManagedPlan {
	t.Helper()
	out, err := f.writer.Create(f.ctx, f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", in), in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// patch 直接构造请求体的原始 JSON——本接口靠「键有没有出现」区分保持原样与显式置空，
// 所以测试也必须走同一条路：先序列化，再解出结构体，跟 handler 的顺序一致。
func (f *planFixture) patch(t *testing.T, id int64, body map[string]any) (ManagedPlan, error) {
	t.Helper()
	body["operation_id"] = uuid.NewString()
	body["reason"] = "调整套餐"
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var in PlanPatch
	if err = json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/v1/plans/%d", id)
	p := f.proof(t, 0, actionPlanUpdate, path, string(raw))
	p.RequestHash = RequestHash("PATCH", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return f.writer.Update(f.ctx, p, id, in, raw)
}

func mustID(t *testing.T, raw string) int64 {
	t.Helper()
	id, err := positiveApprovalID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPlanCreateIsAlwaysDraftIntegration(t *testing.T) {
	f := newPlanFixture(t)
	out := f.create(t, timePlan("STD_1M"))
	if out.Status != "DRAFT" {
		t.Fatalf("新建套餐必须是草稿，实际 %s", out.Status)
	}
	if out.Version != 1 || out.Price != "9.900000" || out.PeriodQuotaBytes == nil ||
		*out.PeriodQuotaBytes != "214748364800" {
		t.Fatalf("新建结果不符：%+v", out)
	}
	// 不属于 TIME 的列必须是 NULL，否则数据库的 chk_plan_shape 形同虚设。
	if out.PackBytes != nil || out.ValidityDays != nil || out.ExtraDevices != nil {
		t.Fatalf("写进了不属于该类型的字段：%+v", out)
	}

	dup := timePlan("STD_1M")
	_, err := f.writer.Create(f.ctx, f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", dup), dup)
	nodeCode(t, err, httpx.ErrConflict)

	// 新建的套餐对 App 不可见——List 只返回 ON_SALE。
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM plans WHERE status='ON_SALE'`); n != 0 {
		t.Fatalf("新建套餐直接上架了：%d 条", n)
	}
}

func TestPlanCreateRejectsWrongShapeIntegration(t *testing.T) {
	f := newPlanFixture(t)
	days, devices, extra := 30, 3, 2
	pack, quota := "107374182400", "214748364800"

	cases := map[string]func(in *PlanCreate){
		"TIME 少了时长":    func(in *PlanCreate) { in.DurationDays = nil },
		"TIME 少了设备数":   func(in *PlanCreate) { in.DeviceLimit = nil },
		"TIME 混进了流量包":  func(in *PlanCreate) { in.PackBytes = &pack },
		"TIME 混进了有效期":  func(in *PlanCreate) { in.ValidityDays = &days },
		"TIME 混进了加购台数": func(in *PlanCreate) { in.ExtraDevices = &extra },
		"TRAFFIC 少了有效期": func(in *PlanCreate) {
			in.PlanType, in.PackBytes, in.DurationDays, in.PeriodQuotaBytes = "TRAFFIC", &pack, nil, nil
		},
		"TRAFFIC 混进了周期配额": func(in *PlanCreate) {
			in.PlanType, in.PackBytes, in.ValidityDays, in.DurationDays = "TRAFFIC", &pack, &days, nil
		},
		"DEVICE_ADDON 混进了设备数": func(in *PlanCreate) {
			in.PlanType, in.ExtraDevices, in.DurationDays, in.PeriodQuotaBytes = "DEVICE_ADDON", &extra, nil, nil
		},
		"价格不是 0.1 的整数倍": func(in *PlanCreate) { in.Price = "9.990000" },
		"价格为零":          func(in *PlanCreate) { in.Price = "0.000000" },
		"价格是负数":         func(in *PlanCreate) { in.Price = "-9.900000" },
		"价格不是数字":        func(in *PlanCreate) { in.Price = "九块九" },
		"配额是零":          func(in *PlanCreate) { z := "0"; in.PeriodQuotaBytes = &z },
		"配额超出安全整数":      func(in *PlanCreate) { z := "9007199254740992"; in.PeriodQuotaBytes = &z },
		"配额带前导零":        func(in *PlanCreate) { z := "007"; in.PeriodQuotaBytes = &z },
		"编码是小写":         func(in *PlanCreate) { in.Code = "std_1m" },
		"编码以数字开头":       func(in *PlanCreate) { in.Code = "1M_STD" },
		"编码太短":          func(in *PlanCreate) { in.Code = "A" },
		"等级越界":          func(in *PlanCreate) { in.Tier = 10 },
		"设备数越界":         func(in *PlanCreate) { n := 101; in.DeviceLimit = &n },
		"类型不存在":         func(in *PlanCreate) { in.PlanType = "LIFETIME" },
		"名称是空白":         func(in *PlanCreate) { in.Name = "   " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := timePlan("STD_1M")
			in.DeviceLimit, in.DurationDays, in.PeriodQuotaBytes = &devices, &days, &quota
			mutate(&in)
			_, err := f.writer.Create(f.ctx,
				f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", in), in)
			nodeCode(t, err, httpx.ErrBadRequest)
		})
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM plans`); n != 0 {
		t.Fatalf("被拒的请求写进了 %d 条", n)
	}
	// 幂等表也不该留下任何痕迹——否则换个 operation_id 才能重试。
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_plan_mutations`); n != 0 {
		t.Fatalf("被拒的请求写了 %d 条幂等记录", n)
	}
}

func TestPlanUpdateBumpsVersionOnlyForSubstanceIntegration(t *testing.T) {
	f := newPlanFixture(t)
	created := f.create(t, timePlan("STD_1M"))
	id := mustID(t, created.ID)

	// 外观字段：改了也不能动 version——否则每次改个角标都会打断进行中的下单。
	cosmetic := []map[string]any{
		{"name": "标准版 30 天"}, {"badge": "限时"}, {"sort_order": 5}, {"badge": nil},
	}
	for _, body := range cosmetic {
		out, err := f.patch(t, id, body)
		if err != nil {
			t.Fatalf("%v：%v", body, err)
		}
		if out.Version != 1 {
			t.Fatalf("外观改动动了版本号：%v → version=%d", body, out.Version)
		}
	}

	// 实质字段：每改一次必须 +1。
	substantive := []map[string]any{
		{"price": "12.900000"}, {"tier": 2}, {"duration_days": 90}, {"device_limit": 5},
		{"period_quota_bytes": "322122547200"},
	}
	want := 1
	for _, body := range substantive {
		want++
		out, err := f.patch(t, id, body)
		if err != nil {
			t.Fatalf("%v：%v", body, err)
		}
		if out.Version != want {
			t.Fatalf("实质改动没有 +1：%v → 期望 %d 实际 %d", body, want, out.Version)
		}
	}

	// 显式 null = 改成不限量，且这也是实质改动。
	out, err := f.patch(t, id, map[string]any{"period_quota_bytes": nil})
	if err != nil {
		t.Fatal(err)
	}
	want++
	if out.PeriodQuotaBytes != nil || out.Version != want {
		t.Fatalf("显式 null 未生效：quota=%v version=%d", out.PeriodQuotaBytes, out.Version)
	}
	// 再提交一次同样的值不算改动：不该白白作废别人的报价。
	out, err = f.patch(t, id, map[string]any{"period_quota_bytes": nil, "name": "标准版"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != want {
		t.Fatalf("没有实质变化却 +1 了：version=%d", out.Version)
	}
	// 键没出现 = 保持原样，不会被当成「改成不限量」。
	out, err = f.patch(t, id, map[string]any{"name": "标准版 A"})
	if err != nil || out.PeriodQuotaBytes != nil || out.Version != want {
		t.Fatalf("省略字段被当成了修改：%+v %v", out, err)
	}

	// code 与 plan_type 不在 PlanPatch 里：带上它们的请求体连解码都过不去。
	for _, body := range []string{`{"code":"NEW_CODE"}`, `{"plan_type":"TRAFFIC"}`, `{"status":"ON_SALE"}`} {
		var in PlanPatch
		if err = decodeApprovalObject([]byte(body), &in); err == nil {
			t.Fatalf("%s 不该被接受", body)
		}
	}
	// 一个可改字段都没给，也要明确拒绝而不是空转一趟。
	if _, err = f.patch(t, id, map[string]any{}); err == nil {
		t.Fatal("空的修改请求被接受了")
	}
}

// 这个用例证明 version 那条规矩**真的有后果**：改价之后未使用的报价下不了单了。
func TestPlanPriceChangeInvalidatesOutstandingQuoteIntegration(t *testing.T) {
	f := newPlanFixture(t)
	created := f.create(t, timePlan("STD_1M"))
	id := mustID(t, created.ID)
	if err := f.writer.SetStatus(f.ctx, f.planProof(t, actionPlanStatus, "POST",
		fmt.Sprintf("/admin/v1/plans/%d/status", id),
		PlanStatusInput{Status: "ON_SALE"}),
		id, PlanStatusInput{OperationID: uuid.NewString(), Status: "ON_SALE", Reason: "开卖"}); err != nil {
		t.Fatal(err)
	}

	quote := func(t *testing.T) string {
		t.Helper()
		var qid string
		if err := f.owner.QueryRow(f.ctx, `INSERT INTO plan_quotes(user_id,plan_id,plan_version,
 base_amount_micro,detail,expires_at) SELECT $1,$2,p.version,9900000,'{}'::jsonb,$3
 FROM plans p WHERE p.id=$2 RETURNING id::text`,
			f.userID, id, f.clock.Now().Add(10*time.Minute)).Scan(&qid); err != nil {
			t.Fatal(err)
		}
		return qid
	}
	use := func(t *testing.T, qid string) error {
		t.Helper()
		tx, err := f.pool.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(f.ctx)
		_, err = plan.LockQuote(f.ctx, tx, qid, f.userID, f.clock.Now())
		return err
	}

	// 先确认这张报价本来是能用的，否则后面的断言毫无意义。
	fresh := quote(t)
	if err := use(t, fresh); err != nil {
		t.Fatalf("新报价本应可用：%v", err)
	}

	// 只改角标：同一张报价必须仍然可用。
	cosmetic := quote(t)
	if _, err := f.patch(t, id, map[string]any{"badge": "热卖"}); err != nil {
		t.Fatal(err)
	}
	if err := use(t, cosmetic); err != nil {
		t.Fatalf("只改角标不该打断进行中的下单：%v", err)
	}

	// 改价：这张报价必须当场失效，且错误是「套餐已改价」而不是别的。
	stale := quote(t)
	if _, err := f.patch(t, id, map[string]any{"price": "12.900000"}); err != nil {
		t.Fatal(err)
	}
	err := use(t, stale)
	nodeCode(t, err, httpx.ErrPlanChanged)

	// 下架同样让报价失效——它走的是 status 那一路，不需要动 version。
	afterPrice := quote(t)
	before := authCount(t, f.svc.Auth, `SELECT version FROM plans WHERE id=`+strconv.FormatInt(id, 10))
	off := PlanStatusInput{OperationID: uuid.NewString(), Status: "OFF_SALE", Reason: "暂停销售"}
	if err = f.writer.SetStatus(f.ctx, f.planProof(t, actionPlanStatus, "POST",
		fmt.Sprintf("/admin/v1/plans/%d/status", id), off), id, off); err != nil {
		t.Fatal(err)
	}
	nodeCode(t, use(t, afterPrice), httpx.ErrPlanChanged)
	if after := authCount(t, f.svc.Auth, `SELECT version FROM plans WHERE id=`+
		strconv.FormatInt(id, 10)); after != before {
		t.Fatalf("上下架不该改版本号：%d → %d", before, after)
	}
}

func TestPlanStatusTransitionsIntegration(t *testing.T) {
	f := newPlanFixture(t)
	id := mustID(t, f.create(t, timePlan("STD_1M")).ID)
	path := fmt.Sprintf("/admin/v1/plans/%d/status", id)
	set := func(t *testing.T, status string) error {
		t.Helper()
		in := PlanStatusInput{OperationID: uuid.NewString(), Status: status, Reason: "状态变更：" + status}
		return f.writer.SetStatus(f.ctx, f.planProof(t, actionPlanStatus, "POST", path, in), id, in)
	}
	for _, status := range []string{"ON_SALE", "OFF_SALE", "ON_SALE"} {
		if err := set(t, status); err != nil {
			t.Fatalf("设为 %s 失败：%v", status, err)
		}
		var got string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM plans WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Fatalf("状态未生效：期望 %s 实际 %s", status, got)
		}
	}
	// 重复设为当前状态是幂等的。
	if err := set(t, "ON_SALE"); err != nil {
		t.Fatalf("重复设置应当幂等：%v", err)
	}
	// 回到 DRAFT 不行：上过架的套餐可能已有报价与订单引用它。
	nodeCode(t, set(t, "DRAFT"), httpx.ErrBadRequest)
	nodeCode(t, set(t, "HIDDEN"), httpx.ErrBadRequest)

	missing := PlanStatusInput{OperationID: uuid.NewString(), Status: "ON_SALE", Reason: "不存在的套餐"}
	mp := f.planProof(t, actionPlanStatus, "POST", "/admin/v1/plans/999999/status", missing)
	nodeCode(t, f.writer.SetStatus(f.ctx, mp, 999999, missing), httpx.ErrNotFound)
}

func TestPlanWriteRequiresStepUpAndOwnPermissionIntegration(t *testing.T) {
	f := newPlanFixture(t)
	in := timePlan("STD_1M")

	bare := f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", in)
	bare.Token = ""
	_, err := f.writer.Create(f.ctx, bare, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	wrong := f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", in)
	wrong.RequestHash = RequestHash("POST", "/admin/v1/plans", []byte("{}"))
	_, err = f.writer.Create(f.ctx, wrong, in)
	nodeCode(t, err, httpx.ErrStepUpInvalid)

	// 新建、修改、上下架是三个独立能力：撤掉新建不影响另外两个。
	id := mustID(t, f.create(t, in).ID)
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='plans.create'`, f.role)
	second := timePlan("STD_12M")
	_, err = f.writer.Create(f.ctx, f.planProof(t, actionPlanCreate, "POST", "/admin/v1/plans", second), second)
	nodeCode(t, err, httpx.ErrForbidden)
	if _, err = f.patch(t, id, map[string]any{"name": "改个名"}); err != nil {
		t.Fatalf("plans.update 不该受 plans.create 影响：%v", err)
	}
	on := PlanStatusInput{OperationID: uuid.NewString(), Status: "ON_SALE", Reason: "开卖"}
	if err = f.writer.SetStatus(f.ctx, f.planProof(t, actionPlanStatus, "POST",
		fmt.Sprintf("/admin/v1/plans/%d/status", id), on), id, on); err != nil {
		t.Fatalf("plans.status 不该受 plans.create 影响：%v", err)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM plans`); n != 1 {
		t.Fatalf("被拒的新建写进了库：共 %d 条", n)
	}
}

func TestPlanListShowsDraftsAndRendersBytesAsStringsIntegration(t *testing.T) {
	f := newPlanFixture(t)
	draft := f.create(t, timePlan("STD_1M"))
	pack, validity, devices := "107374182400", 90, 3
	traffic := PlanCreate{OperationID: uuid.NewString(), Reason: "上新流量包", Code: "PACK_100G",
		Name: "100 GB 流量包", PlanType: "TRAFFIC", Tier: 1, Price: "12.900000",
		PackBytes: &pack, ValidityDays: &validity, DeviceLimit: &devices, SortOrder: 30}
	f.create(t, traffic)
	extra := 1
	f.create(t, PlanCreate{OperationID: uuid.NewString(), Reason: "上新加购", Code: "DEV_ADDON_1",
		Name: "加购 1 台设备", PlanType: "DEVICE_ADDON", Tier: 1, Price: "2.000000",
		ExtraDevices: &extra, SortOrder: 40})
	p := f.people[0]

	list, err := f.read.List(f.ctx, p, "")
	if err != nil {
		t.Fatal(err)
	}
	// 后台要看的正是 App 看不到的那些：三条都是 DRAFT。
	if len(list.Items) != 3 {
		t.Fatalf("列表应当包含草稿：%d 条", len(list.Items))
	}
	if list.Items[0].Code != "STD_1M" || list.Items[2].Code != "DEV_ADDON_1" {
		t.Fatalf("排序不符：%s %s", list.Items[0].Code, list.Items[2].Code)
	}
	// 字节数必须是带引号的字符串，不能退化成 JSON number。
	raw, err := json.Marshal(list.Items[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pack_bytes":"107374182400"`) {
		t.Fatalf("字节数不是字符串：%s", raw)
	}
	if !strings.Contains(string(raw), `"price":"12.900000"`) {
		t.Fatalf("金额不是固定 6 位小数的字符串：%s", raw)
	}

	filtered, err := f.read.List(f.ctx, p, "ON_SALE")
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("状态筛选不符：%v %d", err, len(filtered.Items))
	}
	if _, err = f.read.List(f.ctx, p, "HIDDEN"); err == nil {
		t.Fatal("非法状态被接受了")
	}

	// 撤掉 plans.read 之后一条都不许读，哪怕还握着三个写权限。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='plans.read'`, f.role)
	_, err = f.read.List(f.ctx, p, "")
	nodeCode(t, err, httpx.ErrForbidden)
	_ = draft
}
