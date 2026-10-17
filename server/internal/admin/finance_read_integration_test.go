package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 资金只读的测试重点：金额必须是字符串且精确、不该返回的列一列都不能漏出去、
// 权限要单独授予、筛选是精确匹配不是模糊。

type financeFixture struct {
	*managedUserFixture
	svcFinance *FinanceReadService
	orderNo    string
	txID       int64
}

func newFinanceFixture(t *testing.T) *financeFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"plans", "receive_addresses", "chain_transfers", "orders",
		"deposit_exceptions", "chain_scan_cursors"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}
	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00026_admin_finance_read.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='finance.read' AND risk_level=0`) != 1 {
		t.Fatal("finance.read 能力未定义")
	}
	for _, table := range []string{"plans", "receive_addresses", "chain_transfers", "orders",
		"deposit_exceptions", "chain_scan_cursors"} {
		f.write(t, "REVOKE ALL ON "+pgx.Identifier{table}.Sanitize()+" FROM "+role+
			"; GRANT SELECT ON "+pgx.Identifier{table}.Sanitize()+" TO "+role)
	}

	now := f.clock.Now()
	f.write(t, `INSERT INTO plans(id,code,name,plan_type,tier,duration_days,device_limit,price_micro,status)
 OVERRIDING SYSTEM VALUE VALUES(1,'m1','月付','TIME',1,30,3,167000000,'ON_SALE')`)
	f.write(t, `INSERT INTO receive_addresses(id,network,address,label,signature,sig_key_id,valid_until,status,created_at,updated_at)
 OVERRIDING SYSTEM VALUE VALUES(1,'MOCK','MOCKADDR1','主收款','SIGNATURE-MUST-NOT-LEAK','fund-dev-1',$1,'ACTIVE',$2,$2)`,
		now.Add(24*time.Hour), now)
	f.write(t, `INSERT INTO chain_transfers(id,network,tx_hash,event_index,contract_address,from_address,
 to_address,receive_address_id,amount_micro,block_number,block_time,confirm_status,match_status,
 data_source,raw,created_at) OVERRIDING SYSTEM VALUE
 VALUES(1,'MOCK','0xdeadbeef',0,'CONTRACT','FROMADDR','MOCKADDR1',1,167604300,12345,$1,'SOLIDIFIED',
 'MATCHED','fixture','{"raw_payload_must_not_leak":true}'::jsonb,$1)`, now)
	fx := &financeFixture{managedUserFixture: f, svcFinance: &FinanceReadService{Auth: f.svc.Auth},
		orderNo: "ORD20320102000001", txID: 1}
	f.write(t, `INSERT INTO orders(id,order_no,user_id,order_type,plan_id,plan_snapshot,base_amount_micro,
 pay_amount_micro,pay_method,receive_address_id,status,chain_transfer_id,client_idem_key,
 expires_at,paid_at,completed_at,created_at,updated_at) OVERRIDING SYSTEM VALUE
 VALUES(1,$1,$2,'PLAN',1,'{"plan_code":"m1"}'::jsonb,167000000,167604300,'USDT_TRC20',1,'COMPLETED',1,
 'idem-key-internal',$3,$4,$5,$6,$6)`,
		fx.orderNo, f.userID, now.Add(time.Hour), now.Add(time.Minute), now.Add(2*time.Minute), now)
	f.write(t, `UPDATE chain_transfers SET order_id=1 WHERE id=1`)
	f.write(t, `INSERT INTO deposit_exceptions(id,chain_transfer_id,reason,suggested_order_id,status,created_at)
 OVERRIDING SYSTEM VALUE VALUES(1,1,'AMOUNT_MISMATCH',1,'OPEN',$1)`, now)
	f.write(t, `INSERT INTO chain_scan_cursors(receive_address_id,last_block_time,last_block_number,
 last_success_at,updated_at) VALUES(1,$1,12345,$1,$1)`, now)
	return fx
}

func (f *financeFixture) grantFinance(t *testing.T) Principal {
	t.Helper()
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'finance.read')
 ON CONFLICT DO NOTHING`, f.role)
	return f.people[0]
}

func TestFinanceReadRequiresItsOwnPermissionIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.people[0]
	// users.read 不蕴含 finance.read。
	_, err := f.svcFinance.Orders(f.ctx, p, FinanceFilter{})
	nodeCode(t, err, httpx.ErrForbidden)

	f.grantFinance(t)
	if _, err = f.svcFinance.Orders(f.ctx, p, FinanceFilter{}); err != nil {
		t.Fatal(err)
	}
	// 会话吊销后立刻失效。
	f.write(t, `UPDATE admin_sessions SET revoked_at=$1 WHERE id=$2`, f.clock.Now(), p.SessionID)
	_, err = f.svcFinance.Orders(f.ctx, p, FinanceFilter{})
	nodeCode(t, err, httpx.ErrForbidden)
}

func TestFinanceOrderAmountsAreExactStringsIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)

	page, err := f.svcFinance.Orders(f.ctx, p, FinanceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != "1" {
		t.Fatalf("订单列表不符：%+v", page)
	}
	o := page.Items[0]
	// 167604300 微单位 = 167.604300 USDT，必须一位不差，且是字符串。
	if o.PayAmount != "167.604300" || o.BaseAmount != "167.000000" || o.Currency != "USDT" {
		t.Fatalf("金额不符：%s / %s", o.BaseAmount, o.PayAmount)
	}
	blob, _ := json.Marshal(page)
	// JSON 里金额字段必须带引号；出现裸数字说明被当成浮点了。
	if !strings.Contains(string(blob), `"pay_amount":"167.604300"`) {
		t.Fatalf("金额不是字符串：%s", blob)
	}
	// 内部字段不得外泄。
	for _, leak := range []string{"idem-key-internal", "client_idem_key", "quote_id"} {
		if strings.Contains(string(blob), leak) {
			t.Fatalf("订单列表泄露了 %q", leak)
		}
	}
}

func TestFinanceOrderDetailTimelineAndSnapshotIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)

	out, err := f.svcFinance.Order(f.ctx, p, f.orderNo)
	if err != nil {
		t.Fatal(err)
	}
	// 时间线只列已经发生的节点，且按顺序。
	want := []string{"CREATED", "EXPIRES", "PAID", "COMPLETED"}
	if len(out.Timeline) != len(want) {
		t.Fatalf("时间线条数不符：%+v", out.Timeline)
	}
	for i, e := range out.Timeline {
		if e.Event != want[i] {
			t.Fatalf("时间线顺序不符：%+v", out.Timeline)
		}
	}
	if out.ChainTransfer == nil || out.ChainTransfer.TxHash != "0xdeadbeef" {
		t.Fatalf("未关联链上转账：%+v", out.ChainTransfer)
	}
	if out.ChainTransfer.Amount != "167.604300" {
		t.Fatalf("链上金额不符：%s", out.ChainTransfer.Amount)
	}
	var snapshot map[string]any
	if err = json.Unmarshal(out.PlanSnapshot, &snapshot); err != nil || snapshot["plan_code"] != "m1" {
		t.Fatalf("套餐快照不符：%s", out.PlanSnapshot)
	}
	// 原始链上载荷绝不外泄。
	blob, _ := json.Marshal(out)
	if strings.Contains(string(blob), "raw_payload_must_not_leak") {
		t.Fatal("详情泄露了原始链上载荷")
	}

	// 未支付的订单时间线里不得出现 PAID / COMPLETED。
	f.write(t, `UPDATE orders SET status='PENDING',paid_at=NULL,completed_at=NULL WHERE order_no=$1`, f.orderNo)
	out, err = f.svcFinance.Order(f.ctx, p, f.orderNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Timeline) != 2 {
		t.Fatalf("未支付订单不该有 PAID/COMPLETED：%+v", out.Timeline)
	}
	// 订单不存在。
	_, err = f.svcFinance.Order(f.ctx, p, "NO-SUCH-ORDER")
	nodeCode(t, err, httpx.ErrNotFound)
}

func TestFinanceOrderFiltersAreExactIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)

	// 订单号精确匹配：前缀不得命中。
	page, err := f.svcFinance.Orders(f.ctx, p, FinanceFilter{OrderNo: f.orderNo[:8]})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("订单号做了模糊匹配：%+v", page.Items)
	}
	page, err = f.svcFinance.Orders(f.ctx, p, FinanceFilter{OrderNo: f.orderNo})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("精确订单号未命中：%v %+v", err, page.Items)
	}
	// 状态与用户筛选。
	page, err = f.svcFinance.Orders(f.ctx, p, FinanceFilter{Status: "PENDING"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("状态筛选不符：%v %+v", err, page.Items)
	}
	page, err = f.svcFinance.Orders(f.ctx, p, FinanceFilter{UserID: fmt.Sprint(f.userID + 999)})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("用户筛选不符：%v %+v", err, page.Items)
	}
	// 非法筛选值一律拒绝，不静默忽略。
	for _, bad := range []FinanceFilter{{Status: "WHATEVER"}, {UserID: "0"}, {UserID: "abc"},
		{OrderNo: strings.Repeat("x", 25)}} {
		if _, err = f.svcFinance.Orders(f.ctx, p, bad); err == nil {
			t.Fatalf("非法筛选被接受了：%+v", bad)
		}
	}
}

func TestFinanceChainTransfersAndExceptionsIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)

	page, err := f.svcFinance.ChainTransfers(f.ctx, p, FinanceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].BlockNumber != "12345" {
		t.Fatalf("链上转账列表不符：%+v", page.Items)
	}
	if page.Items[0].OrderNo == nil || *page.Items[0].OrderNo != f.orderNo {
		t.Fatalf("未关联订单号：%+v", page.Items[0].OrderNo)
	}
	blob, _ := json.Marshal(page)
	if strings.Contains(string(blob), "raw_payload_must_not_leak") {
		t.Fatal("链上转账列表泄露了原始载荷")
	}
	// 精确按哈希查。
	page, err = f.svcFinance.ChainTransfers(f.ctx, p, FinanceFilter{TxHash: "0xdead"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("交易哈希做了模糊匹配：%v %+v", err, page.Items)
	}
	if _, err = f.svcFinance.ChainTransfers(f.ctx, p, FinanceFilter{MatchStatus: "NOPE"}); err == nil {
		t.Fatal("非法匹配状态被接受了")
	}

	ex, err := f.svcFinance.DepositExceptions(f.ctx, p, FinanceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Items) != 1 || ex.Items[0].Reason != "AMOUNT_MISMATCH" {
		t.Fatalf("到账异常列表不符：%+v", ex.Items)
	}
	// 疑似订单只是线索，必须如实带出订单号供人核对。
	if ex.Items[0].SuggestedOrderNo == nil || *ex.Items[0].SuggestedOrderNo != f.orderNo {
		t.Fatalf("疑似订单号不符：%+v", ex.Items[0].SuggestedOrderNo)
	}
	if ex.Items[0].ChainTransfer.Amount != "167.604300" {
		t.Fatalf("异常里的链上金额不符：%s", ex.Items[0].ChainTransfer.Amount)
	}
	// 状态筛选必须用库里真实存在的终态。契约原先写的 RESOLVED 库里从来没有过，
	// 按它筛永远返回零条且不报错——看起来只是「没有已处理的」。现在它是非法值。
	filtered, err := f.svcFinance.DepositExceptions(f.ctx, p, FinanceFilter{Status: "REJECTED"})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("异常状态筛选不符：%v %+v", err, filtered.Items)
	}
	if _, err = f.svcFinance.DepositExceptions(f.ctx, p, FinanceFilter{Status: "RESOLVED"}); err == nil {
		t.Fatal("库里不存在的状态值仍被当成合法筛选条件")
	}
}

func TestFinanceReceiveAddressesNeverLeakSignatureIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)

	out, err := f.svcFinance.ReceiveAddresses(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].Address != "MOCKADDR1" || out.Items[0].SigKeyID != "fund-dev-1" {
		t.Fatalf("收款地址列表不符：%+v", out.Items)
	}
	// 签名原文一个字都不能出现。
	blob, _ := json.Marshal(out)
	if strings.Contains(string(blob), "SIGNATURE-MUST-NOT-LEAK") || strings.Contains(string(blob), "signature") {
		t.Fatalf("收款地址列表泄露了签名：%s", blob)
	}

	cursors, err := f.svcFinance.ScanCursors(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors.Items) != 1 || cursors.Items[0].Address != "MOCKADDR1" {
		t.Fatalf("扫描游标不符：%+v", cursors.Items)
	}
	if cursors.Items[0].LastBlockNumber == nil || *cursors.Items[0].LastBlockNumber != "12345" {
		t.Fatalf("游标区块号不符：%+v", cursors.Items[0].LastBlockNumber)
	}
	// 游标从未成功过时如实返回 null，不替人判断「是否落后」。
	f.write(t, `UPDATE chain_scan_cursors SET last_block_number=NULL,last_success_at=NULL`)
	cursors, err = f.svcFinance.ScanCursors(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if cursors.Items[0].LastBlockNumber != nil || cursors.Items[0].LastSuccessAt != nil {
		t.Fatalf("空游标未如实返回：%+v", cursors.Items[0])
	}
}

func TestFinanceReadCannotSeeUnauthorizedColumnsIntegration(t *testing.T) {
	f := newFinanceFixture(t)
	p := f.grantFinance(t)
	// 业务角色对这几张表只有 SELECT，不得有任何写权限——只读接口不该能改账。
	for _, stmt := range []string{
		`UPDATE orders SET status='CANCELLED' WHERE order_no=$1`,
		`DELETE FROM chain_transfers WHERE id=1`,
		`INSERT INTO deposit_exceptions(chain_transfer_id,reason) VALUES(1,'X')`,
	} {
		if _, err := f.pool.Exec(f.ctx, stmt, f.orderNo); err == nil {
			t.Fatalf("业务角色能写资金表：%s", stmt)
		}
	}
	if _, err := f.svcFinance.Orders(f.ctx, p, FinanceFilter{}); err != nil {
		t.Fatalf("只读仍应正常：%v", err)
	}
}
