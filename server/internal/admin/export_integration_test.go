package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 导出这一节要守住的，是方案 12.4 对导出提的三条：行为需 L2、文件带水印、24 小时后删除。
// 这三条分别由「发起走 securedMutation + 下载单独再要一次二次验证」「水印是 NOT NULL 列
// 且逐字出现在文件首末行」「PurgeExports 按 expires_at 清空」承担，下面逐条钉住。
//
// 另外两件容易写错、写错了又不会报错的事，也各有用例：
//   - 闭区间。`to` 当天的数据必须在，次日的必须不在。写成开区间会让每次导出都少一天。
//   - 脱敏。导出件里不能出现 client_idem_key、链上 raw、收款地址签名——
//     只读接口守住了这些，导出件如果没守住，等于开了一个后门。

type exportFixture struct {
	*approvalFixture
	svcExport *ExportService
	jobs      *ExportJobs
}

func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()
	a := newApprovalFixture(t)
	role := pgx.Identifier{a.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"orders", "chain_transfers", "traffic_daily",
		"user_balances", "balance_ledger", "subscription_ledger", "users"} {
		// traffic_daily 是分区表，LIKE 出来的是普通表——对导出而言列一致就够了，
		// 本节要验的是投影与区间，不是分区路由。
		if _, err := a.owner.Exec(a.ctx, "CREATE TABLE "+pgx.Identifier{a.schema, table}.Sanitize()+
			" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING DEFAULTS INCLUDING IDENTITY)"); err != nil {
			t.Fatal(err)
		}
	}

	before := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00036_admin_exports.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err = a.owner.Exec(a.ctx, up); err != nil {
		t.Fatal("actual migration", err)
	}
	if authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	// 三个能力必须各自独立存在，等级也必须对：发起与下载是 L2，列表是 L0。
	if n := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE (code='exports.read' AND risk_level=0)
    OR (code IN ('exports.create','exports.download') AND risk_level=2)`); n != 3 {
		t.Fatal("三个导出能力未按预期定义", n)
	}

	for _, sql := range []string{
		"GRANT SELECT,INSERT,UPDATE,DELETE ON " + pgx.Identifier{a.schema, "admin_exports"}.Sanitize() + " TO " + role,
		"GRANT SELECT,INSERT ON " + pgx.Identifier{a.schema, "admin_export_mutations"}.Sanitize() + " TO " + role,
		"GRANT SELECT ON ALL TABLES IN SCHEMA " + pgx.Identifier{a.schema}.Sanitize() + " TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + pgx.Identifier{a.schema}.Sanitize() + " TO " + role,
	} {
		if _, err = a.owner.Exec(a.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.pool.Exec(a.ctx, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'exports.read'),($1,'exports.create'),($1,'exports.download')`, a.role); err != nil {
		t.Fatal(err)
	}
	for i := range a.people {
		a.people[i].Permissions = append(a.people[i].Permissions,
			exportRead, actionExportCreate, actionExportDownload)
	}
	return &exportFixture{approvalFixture: a,
		svcExport: &ExportService{Security: a.svc, Read: &FinanceReadService{Auth: a.svc.Auth}},
		jobs:      &ExportJobs{Pool: a.pool, Clock: a.clock}}
}

func (f *exportFixture) create(t *testing.T, in ExportCreateInput) (ManagedExport, error) {
	t.Helper()
	if in.OperationID == "" {
		in.OperationID = uuid.NewString()
	}
	if in.Reason == "" {
		in.Reason = "月度对账"
	}
	body, _ := json.Marshal(in)
	p := f.proof(t, 0, actionExportCreate, "/admin/v1/exports", string(body))
	return f.svcExport.RequestExport(f.ctx, p, in)
}

func (f *exportFixture) download(t *testing.T, person int, id int64) (ExportFile, error) {
	t.Helper()
	in := ExportDownloadInput{Reason: "交财务核对"}
	body, _ := json.Marshal(in)
	path := "/admin/v1/exports/" + strconv.FormatInt(id, 10) + "/download"
	p := f.proof(t, person, actionExportDownload, path, string(body))
	return f.svcExport.Download(f.ctx, p, id, in)
}

// seedOrder 造一笔订单。day 是 created_at 的日期；哨兵串用来验证脱敏。
func (f *exportFixture) seedOrder(t *testing.T, no string, day time.Time) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO users(id,account_no,secret_lookup,invite_code,created_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		int64(4242), "4242424242", strings.Repeat("c", 64), "INVEXP", f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO orders(order_no,user_id,order_type,
 base_amount_micro,pay_amount_micro,pay_method,client_idem_key,expires_at,created_at,updated_at)
 VALUES($1,4242,'TOPUP',1000000,1234567,'USDT_TRC20',$2,$3,$4,$4)`,
		no, "private-idem-sentinel-"+no, day.Add(time.Hour), day); err != nil {
		t.Fatal(err)
	}
}

func (f *exportFixture) statusOf(t *testing.T, id int64) (string, *string) {
	t.Helper()
	var status string
	var reason *string
	if err := f.pool.QueryRow(f.ctx, `SELECT status,failure_reason FROM admin_exports WHERE id=$1`, id).
		Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	return status, reason
}

func exportID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestExportRangeRulesIntegration：区间规则。RECONCILE 带区间必须拒绝而不是静默忽略，
// 三种周期类不带区间必须拒绝，跨度上限要真的挡住，而且**任何一次拒绝都不能留下记录**。
func TestExportRangeRulesIntegration(t *testing.T) {
	f := newExportFixture(t)
	for _, bad := range []ExportCreateInput{
		{Kind: "RECONCILE", From: "2026-01-01", To: "2026-01-02"},
		{Kind: "RECONCILE", To: "2026-01-02"},
		{Kind: "ORDERS"},
		{Kind: "ORDERS", From: "2026-01-01"},
		{Kind: "ORDERS", From: "2026-01-02", To: "2026-01-01"},
		{Kind: "ORDERS", From: "2026-01-01", To: "2026-04-05"}, // 95 天
		{Kind: "ORDERS", From: "2026/01/01", To: "2026-01-02"},
		{Kind: "TRAFFIC", From: "2026-01-01", To: "not-a-date"},
		{Kind: "NOPE", From: "2026-01-01", To: "2026-01-02"},
	} {
		_, err := f.create(t, bad)
		approvalCode(t, err, httpx.ErrBadRequest)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_exports`); n != 0 {
		t.Fatalf("被拒绝的请求留下了 %d 条导出记录", n)
	}
	// 边界：92 天整（差 91 天）必须通过，93 天必须拒。
	if _, err := f.create(t, ExportCreateInput{Kind: "ORDERS", From: "2026-01-01", To: "2026-04-01"}); err != nil {
		t.Fatal("92 天整被误拒", err)
	}
	_, err := f.create(t, ExportCreateInput{Kind: "ORDERS", From: "2026-01-01", To: "2026-04-03"})
	approvalCode(t, err, httpx.ErrBadRequest)
}

// TestExportRequestIsIdempotentAndWatermarkedIntegration：发起登记出 PENDING，
// 水印含发起人账号与时间，幂等重放逐字节相同，换请求体是 40901。
func TestExportRequestIsIdempotentAndWatermarkedIntegration(t *testing.T) {
	f := newExportFixture(t)
	op := uuid.NewString()
	in := ExportCreateInput{OperationID: op, Kind: "RECONCILE", Reason: "季度审计"}
	first, err := f.create(t, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "PENDING" || first.RangeFrom != nil || first.RangeTo != nil {
		t.Fatal("RECONCILE 应当登记为无区间的 PENDING", first)
	}
	if first.RowCount != nil || first.ByteSize != nil || first.ContentSHA256 != nil || first.ExpiresAt != nil {
		t.Fatal("还没生成就带上了生成结果", first)
	}
	// 方案 12.4：水印 = 管理员账号 + 时间。
	if !strings.Contains(first.Watermark, "fixture0") ||
		!strings.Contains(first.Watermark, f.clock.Now().UTC().Format("2006-01-02")) {
		t.Fatal("水印里没有发起人账号或时间", first.Watermark)
	}
	if first.RequestedBy != "fixture0" {
		t.Fatal("requested_by 不是发起人账号", first.RequestedBy)
	}

	second, err := f.create(t, in)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Fatal("幂等重放的响应不是逐字节相同")
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_exports`); n != 1 {
		t.Fatalf("同一 operation_id 生成了 %d 条导出", n)
	}
	// 同键换请求体：冲突，且不额外建任务。
	_, err = f.create(t, ExportCreateInput{OperationID: op, Kind: "ORDERS",
		From: "2026-01-01", To: "2026-01-02", Reason: "季度审计"})
	approvalCode(t, err, httpx.ErrConflict)
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_exports`); n != 1 {
		t.Fatal("冲突的请求还是建了任务")
	}
	// 发起必须走二次验证：请求本身完整，只是不带令牌。
	fresh := ExportCreateInput{OperationID: uuid.NewString(), Kind: "RECONCILE", Reason: "季度审计"}
	freshBody, _ := json.Marshal(fresh)
	bare := f.proof(t, 0, actionExportCreate, "/admin/v1/exports", string(freshBody))
	bare.Token = ""
	_, err = f.svcExport.RequestExport(f.ctx, bare, fresh)
	approvalCode(t, err, httpx.ErrStepUpRequired)
}

// TestExportBuildsWatermarkedCsvIntegration：worker 生成之后，文件本身要对——
// BOM、首末水印行、表头、行数、SHA-256，以及**闭区间**与**脱敏**。
func TestExportBuildsWatermarkedCsvIntegration(t *testing.T) {
	f := newExportFixture(t)
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	f.seedOrder(t, "YH-BEFORE", day("2026-02-28").Add(23*time.Hour))
	f.seedOrder(t, "YH-FIRST", day("2026-03-01"))
	// 最后一天的 23:59——闭区间必须收进来。写成 created_at < to 会漏掉整整一天。
	f.seedOrder(t, "YH-LAST", day("2026-03-31").Add(23*time.Hour+59*time.Minute))
	f.seedOrder(t, "YH-AFTER", day("2026-04-01"))

	created, err := f.create(t, ExportCreateInput{Kind: "ORDERS", From: "2026-03-01", To: "2026-03-31"})
	if err != nil {
		t.Fatal(err)
	}
	id := exportID(t, created.ID)
	if err = f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if status, reason := f.statusOf(t, id); status != "READY" {
		t.Fatal("生成后状态不对", status, reason)
	}
	file, err := f.download(t, 0, id)
	if err != nil {
		t.Fatal(err)
	}
	text := string(file.Content)
	if !strings.HasPrefix(text, "\xef\xbb\xbf") {
		t.Fatal("缺少 UTF-8 BOM，Excel 会把中文读成乱码")
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "\xef\xbb\xbf# ") || !strings.Contains(lines[0], created.Watermark) {
		t.Fatal("首行不是水印行", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "# end ") || !strings.Contains(last, created.Watermark) ||
		!strings.Contains(last, "rows=2") {
		t.Fatal("末行不是可用于判断截断的结束水印", last)
	}

	// 中间那段必须是干净的 CSV。
	body := strings.Join(lines[1:len(lines)-1], "\n")
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal("水印之间的正文不是合法 CSV", err)
	}
	if records[0][0] != "order_no" || len(records) != 3 {
		t.Fatal("表头或行数不对", records)
	}
	if records[1][0] != "YH-FIRST" || records[2][0] != "YH-LAST" {
		t.Fatal("闭区间取错了：应当且只应当包含区间内的两笔", records)
	}
	// 金额必须是固定 6 位小数的字符串，与资金只读接口同一口径。
	if records[1][5] != "1.000000" || records[1][6] != "1.234567" {
		t.Fatal("金额没有按 money.Micro 渲染", records[1])
	}
	// 脱敏：只读接口不给的东西，导出件也不能给。
	for _, secret := range []string{"private-idem-sentinel", "client_idem_key"} {
		if strings.Contains(text, secret) {
			t.Fatalf("导出件泄露了 %q", secret)
		}
	}
	// 元数据要和文件对得上。
	sum := sha256.Sum256(file.Content)
	if file.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("下载回来的校验和与内容不符")
	}
	page, err := f.svcExport.Exports(f.ctx, f.people[0], ExportFilter{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("列表", page, err)
	}
	got := page.Items[0]
	if got.RowCount == nil || *got.RowCount != "2" ||
		got.ByteSize == nil || *got.ByteSize != strconv.Itoa(len(file.Content)) ||
		got.ContentSHA256 == nil || *got.ContentSHA256 != file.SHA256 {
		t.Fatal("列表里的行数/字节数/校验和与文件不一致", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(f.clock.Now().Add(exportTTL)) {
		t.Fatal("到期时间不是生成时刻 +24 小时", got.ExpiresAt)
	}
	// 列表接口永远不带内容：序列化结果里不能出现文件里的任何一行。
	blob, _ := json.Marshal(page)
	if bytes.Contains(blob, []byte("YH-FIRST")) {
		t.Fatal("列表接口把文件内容带出来了")
	}
}

// TestExportKindsProduceTheirOwnShapeIntegration：四种 kind 各自的表头与取数口径。
func TestExportKindsProduceTheirOwnShapeIntegration(t *testing.T) {
	f := newExportFixture(t)
	now := f.clock.Now()
	f.seedOrder(t, "YH-RECON", now.Add(-48*time.Hour))
	// 一笔已完成却没有任何发放流水的 PLAN 订单——对账三类异常之一。
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO orders(order_no,user_id,order_type,
 base_amount_micro,pay_amount_micro,pay_method,client_idem_key,status,completed_at,
 expires_at,created_at,updated_at)
 VALUES('YH-NOFULFILL',4242,'PLAN',1000000,1000000,'BALANCE','k2','COMPLETED',$1,$1,$1,$1)`,
		now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 余额与流水对不上。
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO user_balances(user_id,available_micro,updated_at)
 VALUES(4242,500000,$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.owner.Exec(f.ctx, `INSERT INTO traffic_daily(stat_date,device_id,node_id,user_id,
 upload_bytes,download_bytes) VALUES(DATE '2026-03-15',7,9,4242,1024,2048)`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		kind       string
		from, to   string
		wantHeader string
		wantRow    string
	}{
		{"TRAFFIC", "2026-03-01", "2026-03-31", "stat_date,user_id,device_id,node_id", "2026-03-15,4242,7,9,1024,2048,3072"},
		{"RECONCILE", "", "", "category,subject,detail", "ORDER_NO_FULFILL,YH-NOFULFILL"},
		{"CHAIN_TRANSFERS", "2026-03-01", "2026-03-31", "id,network,tx_hash", ""},
	} {
		created, err := f.create(t, ExportCreateInput{Kind: tc.kind, From: tc.from, To: tc.to})
		if err != nil {
			t.Fatal(tc.kind, err)
		}
		if err = f.jobs.RunExports(f.ctx, now); err != nil {
			t.Fatal(tc.kind, err)
		}
		id := exportID(t, created.ID)
		if status, reason := f.statusOf(t, id); status != "READY" {
			t.Fatal(tc.kind, "未生成", status, reason)
		}
		file, err := f.download(t, 0, id)
		if err != nil {
			t.Fatal(tc.kind, err)
		}
		text := string(file.Content)
		if !strings.Contains(text, tc.wantHeader) {
			t.Fatalf("%s 表头不对：%s", tc.kind, text)
		}
		if tc.wantRow != "" && !strings.Contains(text, tc.wantRow) {
			t.Fatalf("%s 缺少预期的行 %q：%s", tc.kind, tc.wantRow, text)
		}
	}
	// 对账那一份还必须同时包含余额不符这一类，否则三类合一就是假的。
	var content []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT content FROM admin_exports
 WHERE kind='RECONCILE' AND status='READY'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "BALANCE_MISMATCH,4242,500000") {
		t.Fatal("对账导出漏掉了余额不符这一类", string(content))
	}
}

// TestExportDownloadGuardsAndAuditIntegration：下载是数据真正离开系统的那一刻。
// 未生成/失败/过期各有各的拒法，每成功一次写一条审计，令牌一次一用。
func TestExportDownloadGuardsAndAuditIntegration(t *testing.T) {
	f := newExportFixture(t)
	pending, err := f.create(t, ExportCreateInput{Kind: "RECONCILE"})
	if err != nil {
		t.Fatal(err)
	}
	pendingID := exportID(t, pending.ID)
	_, err = f.download(t, 0, pendingID)
	approvalCode(t, err, httpx.ErrConflict) // 还在生成中

	if err = f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	auditBefore := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_audit_logs
 WHERE action_code='exports.download' AND result='SUCCESS'`)
	file, err := f.download(t, 0, pendingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Content) == 0 || !strings.HasSuffix(file.Name, ".csv") {
		t.Fatal("下载结果不对", file.Name, len(file.Content))
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_audit_logs
 WHERE action_code='exports.download' AND result='SUCCESS'`); n != auditBefore+1 {
		t.Fatal("一次成功下载没有写出恰好一条审计", n)
	}
	// 再下载一次要再做一次二次验证；令牌是一次性的，重放同一张必须失败。
	in := ExportDownloadInput{Reason: "交财务核对"}
	body, _ := json.Marshal(in)
	path := "/admin/v1/exports/" + pending.ID + "/download"
	p := f.proof(t, 0, actionExportDownload, path, string(body))
	if _, err = f.svcExport.Download(f.ctx, p, pendingID, in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svcExport.Download(f.ctx, p, pendingID, in); err == nil {
		t.Fatal("同一张二次验证令牌被用了两次")
	}
	// 没有二次验证一律 40110。（请求本身是完整的，只是不带令牌——
	// 这样才测的是「缺二次验证」，而不是「请求元数据缺失」。）
	bare := f.proof(t, 0, actionExportDownload, path, string(body))
	bare.Token = ""
	_, err = f.svcExport.Download(f.ctx, bare, pendingID, in)
	approvalCode(t, err, httpx.ErrStepUpRequired)

	// 生成失败的导出不能下载，而且要把失败原因告诉调用方。
	failed, err := f.create(t, ExportCreateInput{Kind: "ORDERS", From: "2026-03-01", To: "2026-03-02"})
	if err != nil {
		t.Fatal(err)
	}
	failedID := exportID(t, failed.ID)
	f.seedOrder(t, "YH-A", exportDay(t, "2026-03-01"))
	f.seedOrder(t, "YH-B", exportDay(t, "2026-03-01").Add(time.Hour))
	// 把行数上限临时压到 1：这是唯一能在测试里真的撞一次上限的办法。
	restore := exportMaxRows
	exportMaxRows = 1
	t.Cleanup(func() { exportMaxRows = restore })
	if err = f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	exportMaxRows = restore
	status, reason := f.statusOf(t, failedID)
	if status != "FAILED" || reason == nil || !strings.Contains(*reason, "缩小区间") {
		t.Fatal("超限没有被记成带原因的 FAILED", status, reason)
	}
	var content []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT content FROM admin_exports WHERE id=$1`, failedID).
		Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != nil {
		t.Fatal("超限的导出还是留下了半份数据")
	}
	_, err = f.download(t, 0, failedID)
	approvalCode(t, err, httpx.ErrConflict)

	// 过了保留期：即便清理任务还没跑到，也必须按过期拒绝。
	if _, err = f.owner.Exec(f.ctx, `UPDATE admin_exports SET expires_at=$2 WHERE id=$1`,
		pendingID, f.clock.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = f.download(t, 0, pendingID)
	approvalCode(t, err, httpx.ErrNotFound)
	// 不存在的导出同样是 40401，不泄露「有没有这么一号」。
	_, err = f.download(t, 0, 9223372036854775807)
	approvalCode(t, err, httpx.ErrNotFound)
}

func exportDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestExportPurgeHonoursRetentionIntegration：方案 12.4 的 24 小时保留期。
// 到期清空内容并转 EXPIRED，记录本身再留 7 天，之后整行删除。
func TestExportPurgeHonoursRetentionIntegration(t *testing.T) {
	f := newExportFixture(t)
	created, err := f.create(t, ExportCreateInput{Kind: "RECONCILE"})
	if err != nil {
		t.Fatal(err)
	}
	id := exportID(t, created.ID)
	if err = f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// 保留期内不能动。
	if err = f.jobs.PurgeExports(f.ctx, f.clock.Now().Add(exportTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.statusOf(t, id); status != "READY" {
		t.Fatal("保留期内的导出被提前清掉了", status)
	}
	// 到点清空内容、转 EXPIRED，但记录还在——审计日志里引用着这个编号。
	if err = f.jobs.PurgeExports(f.ctx, f.clock.Now().Add(exportTTL)); err != nil {
		t.Fatal(err)
	}
	var status string
	var content []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT status,content FROM admin_exports WHERE id=$1`, id).
		Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "EXPIRED" || content != nil {
		t.Fatal("到期后文件没有被删掉", status, len(content))
	}
	if _, err = f.download(t, 0, id); err == nil {
		t.Fatal("过期的导出还能下载")
	}
	// 再过 7 天整行删除。
	if err = f.jobs.PurgeExports(f.ctx, f.clock.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_exports WHERE id=`+
		strconv.FormatInt(id, 10)); n != 0 {
		t.Fatal("超过留存期的导出记录没有被删除")
	}
}

// TestExportJobDrainsQueueAndIsolatesFailuresIntegration：队列里多条时一轮跑完若干条，
// 其中一条失败不影响其他条；空队列不报错。
func TestExportJobDrainsQueueAndIsolatesFailuresIntegration(t *testing.T) {
	f := newExportFixture(t)
	if err := f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal("空队列不该报错", err)
	}
	f.seedOrder(t, "YH-Q1", exportDay(t, "2026-03-01"))
	f.seedOrder(t, "YH-Q2", exportDay(t, "2026-03-02"))
	var ids []int64
	for _, in := range []ExportCreateInput{
		{Kind: "ORDERS", From: "2026-03-01", To: "2026-03-01"}, // 1 行
		{Kind: "ORDERS", From: "2026-03-01", To: "2026-03-02"}, // 2 行，会超限
		{Kind: "RECONCILE"},
	} {
		out, err := f.create(t, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, exportID(t, out.ID))
	}
	restore := exportMaxRows
	exportMaxRows = 1
	if err := f.jobs.RunExports(f.ctx, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	exportMaxRows = restore
	want := []string{"READY", "FAILED", "READY"}
	for i, id := range ids {
		if status, reason := f.statusOf(t, id); status != want[i] {
			t.Fatalf("第 %d 条应当是 %s，实际 %s（%v）", i+1, want[i], status, reason)
		}
	}
}
