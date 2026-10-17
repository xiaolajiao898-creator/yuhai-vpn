package admin

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 到账异常处置是全项目第一个真正**动别人的钱**的后台接口，所以这些用例盯的是钱：
//
//   - 钱到底进了谁的账，进了几次；
//   - 已经处置过的异常绝不允许再动一次；
//   - 审批期间转账被自动结算认领走、或金额变了，执行必须拒绝而不是将错就错；
//   - REJECT 一分钱都不能动。
//
// 记账那一段（订单 → PAID → 先入后扣 → 开通权益 → COMPLETED）走的是
// internal/deposit 的 settleOnto，与自动结算逐行相同的那一份实现；
// 这里验的是「后台这条路把它调对了没有」，不是重新验一遍结算本身。

type exceptionFixture struct {
	*managedUserFixture
	svcResolve  *DepositExceptionService
	read        *FinanceReadService
	payer       int64
	orderID     int64
	orderNo     string
	transferID  int64
	exceptionID int64
	amountMicro int64
	payMicro    int64
}

func newExceptionFixture(t *testing.T) *exceptionFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"orders", "chain_transfers", "deposit_exceptions",
		"receive_addresses", "outbox_events", "traffic_packs", "device_addons"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	// 复制出来的 deposit_exceptions 带着 public 里的全部 CHECK——包括本轮迁移要加的那两条，
	// 如果这台机器的 public 已经跑过 00037。先摘掉，让迁移真的在「加约束之前」的表上执行，
	// 否则测的就不是这条迁移本身了。（与 approvalFixture 模拟 pre-00013 表同一个道理。）
	for _, name := range []string{"deposit_exceptions_resolved_shape", "deposit_exceptions_resolved_approval"} {
		f.write(t, "ALTER TABLE deposit_exceptions DROP CONSTRAINT IF EXISTS "+
			pgx.Identifier{name}.Sanitize())
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	for _, file := range []string{"00026_admin_finance_read.sql",
		"00037_admin_deposit_exception_resolve.sql"} {
		raw, err := os.ReadFile("../db/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		f.write(t, up)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	// L3：风险等级必须是 3，否则 executorFor 的 level 校验形同虚设。
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='finance.deposit_exception' AND risk_level=3`) != 1 {
		t.Fatal("处置能力未定义或风险等级不是 3")
	}

	// 这批授权就是本接口真实需要的权限清单。
	for _, sql := range []string{
		"GRANT SELECT,UPDATE ON deposit_exceptions TO " + role,
		"GRANT SELECT,UPDATE ON orders TO " + role,
		"GRANT SELECT,UPDATE ON chain_transfers TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON user_balances TO " + role,
		"GRANT SELECT,INSERT ON balance_ledger TO " + role,
		"GRANT SELECT,INSERT ON subscription_ledger TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON subscriptions TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON traffic_packs TO " + role,
		"GRANT SELECT,INSERT,UPDATE ON device_addons TO " + role,
		"GRANT SELECT,INSERT ON outbox_events TO " + role,
		"GRANT SELECT ON receive_addresses TO " + role,
		"GRANT UPDATE(status) ON users TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + pgx.Identifier{f.schema}.Sanitize() + " TO " + role,
	} {
		f.write(t, sql)
	}
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'finance.read'),($1,'finance.deposit_exception')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, financeRead, actionExceptionResolve)
	}

	out := &exceptionFixture{managedUserFixture: f,
		svcResolve: &DepositExceptionService{Security: f.svc},
		read:       &FinanceReadService{Auth: f.svc.Auth},
		payer:      9007199254740999,
		// 实付比应付少 0.5 USDT——AMOUNT_MISMATCH 本来就是最常见的异常原因，
		// 用它当默认场景，顺带把「金额不一致照样能处置」这条钉住。
		amountMicro: 9_500_000, payMicro: 10_000_000,
	}
	f.user(t, out.payer, "9000000001", "ACTIVE")
	f.write(t, `INSERT INTO receive_addresses(id,network,address,signature,sig_key_id,valid_until,status)
 OVERRIDING SYSTEM VALUE VALUES(1,'MOCK','MOCKADDR-EXC','sig','k1',$1,'ACTIVE')`,
		f.clock.Now().Add(365*24*time.Hour))
	out.seedException(t, "YH-EXC-0001")
	return out
}

// seedException 造一笔「付到我们地址但匹配不上订单」的钱：一张待付订单 + 一笔 EXCEPTION 转账。
func (f *exceptionFixture) seedException(t *testing.T, orderNo string) {
	t.Helper()
	now := f.clock.Now()
	f.orderNo = orderNo
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO orders(order_no,user_id,order_type,
 base_amount_micro,pay_amount_micro,pay_method,receive_address_id,status,client_idem_key,
 expires_at,created_at,updated_at)
 VALUES($1,$2,$3,$4,$4,'USDT_TRC20',1,'PENDING',$5,$6,$7,$7) RETURNING id`,
		orderNo, f.payer, "TOPUP", f.payMicro, "idem-"+orderNo,
		now.Add(time.Hour), now.Add(-2*time.Hour)).Scan(&f.orderID); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO chain_transfers(network,tx_hash,event_index,
 contract_address,from_address,to_address,receive_address_id,amount_micro,block_number,block_time,
 confirm_status,match_status,data_source,created_at)
 VALUES('MOCK',$1,0,'MOCK','PAYER','MOCKADDR-EXC',1,$2,1,$3,'SOLIDIFIED','EXCEPTION','MOCK',$3)
 RETURNING id`, "tx-"+orderNo, f.amountMicro, now.Add(-time.Hour)).Scan(&f.transferID); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO deposit_exceptions(chain_transfer_id,reason,
 suggested_order_id,status,created_at) VALUES($1,'AMOUNT_MISMATCH',$2,'OPEN',$3) RETURNING id`,
		f.transferID, f.orderID, now.Add(-time.Hour)).Scan(&f.exceptionID); err != nil {
		t.Fatal(err)
	}
}

func (f *exceptionFixture) request(t *testing.T, person int, in ExceptionResolveInput) (Approval, error) {
	t.Helper()
	body, _ := json.Marshal(in)
	path := "/admin/v1/deposit-exceptions/" + strconv.FormatInt(f.exceptionID, 10) + "/resolve"
	p := f.proof(t, person, actionExceptionResolve, path, string(body))
	return f.svcResolve.RequestResolve(f.ctx, p, f.exceptionID, in)
}

// approve 让另一名管理员点通过——钱是在这一刻动的。
func (f *exceptionFixture) approve(t *testing.T, a Approval) (Approval, error) {
	t.Helper()
	id, err := positiveApprovalID(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := f.proof(t, 1, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	return f.svc.Decide(f.ctx, p, id, true, "")
}

func (f *exceptionFixture) balanceOf(t *testing.T, userID int64) int64 {
	t.Helper()
	var micro int64
	err := f.pool.QueryRow(f.ctx, `SELECT COALESCE(available_micro,0) FROM user_balances
 WHERE user_id=$1`, userID).Scan(&micro)
	if err != nil && err.Error() != "no rows in result set" {
		t.Fatal(err)
	}
	return micro
}

func (f *exceptionFixture) exceptionState(t *testing.T) (string, *int64, *time.Time) {
	t.Helper()
	var status string
	var approvalID *int64
	var at *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT status,approval_id,resolved_at FROM deposit_exceptions
 WHERE id=$1`, f.exceptionID).Scan(&status, &approvalID, &at); err != nil {
		t.Fatal(err)
	}
	return status, approvalID, at
}

func (f *exceptionFixture) ledgerCount(t *testing.T) int {
	t.Helper()
	return authCount(t, f.svc.Auth, `SELECT count(*) FROM balance_ledger`)
}

// TestExceptionResolveToBalanceCreditsOnceIntegration：转入余额。
// 钱必须恰好进一次指定账户，异常单转 RESOLVED_TO_BALANCE 并指向那张审批单。
func TestExceptionResolveToBalanceCreditsOnceIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before := f.balanceOf(t, f.payer)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "用户自报付款，核对链上哈希无误"})
	if err != nil {
		t.Fatal(err)
	}
	// 202 时钱**还没有动**。这是 L3 的全部意义。
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("发起审批就把钱记进去了", before, got)
	}
	if status, _, _ := f.exceptionState(t); status != "OPEN" {
		t.Fatal("发起审批就把异常单结掉了", status)
	}

	out, err := f.approve(t, a)
	if err != nil || out.Status != "EXECUTED" {
		t.Fatal("审批通过后未执行", out, err)
	}
	if got := f.balanceOf(t, f.payer); got != before+f.amountMicro {
		t.Fatalf("余额 %d，期望 %d", got, before+f.amountMicro)
	}
	status, approvalID, at := f.exceptionState(t)
	aid, _ := positiveApprovalID(a.ID)
	if status != "RESOLVED_TO_BALANCE" || approvalID == nil || *approvalID != aid || at == nil {
		t.Fatal("异常单收口不对", status, approvalID, at)
	}
	// 流水必须是 EXCEPTION_CREDIT / DEPOSIT_EXCEPTION，且记在处置人名下。
	var entry, source, operator string
	var micro, sourceID int64
	var operatorID *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT entry_type,source_type,source_id,amount_micro,
 operator_type,operator_id FROM balance_ledger WHERE idempotency_key=$1`,
		"deposit_exception:"+strconv.FormatInt(f.exceptionID, 10)).
		Scan(&entry, &source, &sourceID, &micro, &operator, &operatorID); err != nil {
		t.Fatal(err)
	}
	if entry != "EXCEPTION_CREDIT" || source != "DEPOSIT_EXCEPTION" || sourceID != f.exceptionID ||
		micro != f.amountMicro || operator != "ADMIN" || operatorID == nil {
		t.Fatal("流水口径不对", entry, source, sourceID, micro, operator, operatorID)
	}
	// 转账保持 EXCEPTION：它确实没有被记到任何订单上。
	var match string
	var orderID *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT match_status,order_id FROM chain_transfers WHERE id=$1`,
		f.transferID).Scan(&match, &orderID); err != nil {
		t.Fatal(err)
	}
	if match != "EXCEPTION" || orderID != nil {
		t.Fatal("转入余额不该把转账标成已匹配", match, orderID)
	}
	// 已处置的绝不允许翻案——那等于第二次动钱。
	_, err = f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "再来一次"})
	approvalCode(t, err, httpx.ErrOrderState)
	if got := f.balanceOf(t, f.payer); got != before+f.amountMicro {
		t.Fatal("翻案把钱又记了一次")
	}
}

// TestExceptionResolveToOrderSettlesThroughTheSamePathIntegration：转入订单。
// 走的是与自动结算完全相同的那一段，所以订单要 COMPLETED、转账要 MATCHED、
// 先入后扣两条流水都在、outbox 要有事件。金额与应付不一致**不阻止**处置。
func TestExceptionResolveToOrderSettlesThroughTheSamePathIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before := f.balanceOf(t, f.payer)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_ORDER",
		OrderNo: f.orderNo, Note: "少付 0.5，用户已补说明，按原单放行"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.approve(t, a); err != nil || out.Status != "EXECUTED" {
		t.Fatal(out, err)
	}

	var status, fulfillment string
	var linked *int64
	var paidAt, completedAt *time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT status,COALESCE(fulfillment_result,''),chain_transfer_id,
 paid_at,completed_at FROM orders WHERE id=$1`, f.orderID).
		Scan(&status, &fulfillment, &linked, &paidAt, &completedAt); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETED" || linked == nil || *linked != f.transferID ||
		paidAt == nil || completedAt == nil {
		t.Fatal("订单没有走完结算", status, linked, paidAt, completedAt)
	}
	var match string
	var orderID *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT match_status,order_id FROM chain_transfers WHERE id=$1`,
		f.transferID).Scan(&match, &orderID); err != nil {
		t.Fatal(err)
	}
	if match != "MATCHED" || orderID == nil || *orderID != f.orderID {
		t.Fatal("转账没有被记到订单上", match, orderID)
	}
	// TOPUP：只有一条 DEPOSIT 入账，金额是**实付**而不是应付。
	var entry string
	var micro int64
	if err = f.pool.QueryRow(f.ctx, `SELECT entry_type,amount_micro FROM balance_ledger
 WHERE source_type='CHAIN_TRANSFER' AND source_id=$1`, f.transferID).Scan(&entry, &micro); err != nil {
		t.Fatal(err)
	}
	if entry != "DEPOSIT" || micro != f.amountMicro {
		t.Fatalf("入账口径不对：%s %d（实付 %d，应付 %d）", entry, micro, f.amountMicro, f.payMicro)
	}
	if got := f.balanceOf(t, f.payer); got != before+f.amountMicro {
		t.Fatal("余额不对", got)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM outbox_events
 WHERE aggregate_type='order'`); n != 1 {
		t.Fatal("没有写出恰好一条订单完成事件", n)
	}
	if status, _, _ := f.exceptionState(t); status != "RESOLVED_TO_ORDER" {
		t.Fatal("异常单状态不对", status)
	}
}

// TestExceptionRejectMovesNoMoneyIntegration：驳回一分钱都不能动。
func TestExceptionRejectMovesNoMoneyIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before, ledgers := f.balanceOf(t, f.payer), f.ledgerCount(t)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "REJECT",
		Note: "链上来源为已知投毒地址，不入账"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.approve(t, a); err != nil || out.Status != "EXECUTED" {
		t.Fatal(out, err)
	}
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("驳回动了钱", before, got)
	}
	if n := f.ledgerCount(t); n != ledgers {
		t.Fatal("驳回写了资金流水", n, ledgers)
	}
	var orderStatus, match string
	if err = f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, f.orderID).
		Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(f.ctx, `SELECT match_status FROM chain_transfers WHERE id=$1`,
		f.transferID).Scan(&match); err != nil {
		t.Fatal(err)
	}
	// 转账保持 EXCEPTION：改成 MATCHED 会让对账以为它已经有了去处。
	if orderStatus != "PENDING" || match != "EXCEPTION" {
		t.Fatal("驳回改了不该改的状态", orderStatus, match)
	}
	if status, _, _ := f.exceptionState(t); status != "REJECTED" {
		t.Fatal("异常单状态不对", status)
	}
}

// TestExceptionResolveRejectsBadShapeIntegration：动作与字段必须配套，
// 不属于该动作的字段一律拒绝而不是静默忽略，而且**任何一次拒绝都不能建审批单**。
func TestExceptionResolveRejectsBadShapeIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	for _, bad := range []ExceptionResolveInput{
		{Action: "WHATEVER", Note: "x"},
		{Action: "TO_ORDER", Note: "x"},                            // 缺 order_no
		{Action: "TO_BALANCE", Note: "x"},                          // 缺 user_account_no
		{Action: "REJECT", OrderNo: "YH-EXC-0001", Note: "x"},      // REJECT 不该带 order_no
		{Action: "REJECT", UserAccountNo: "9000000001", Note: "x"}, // 也不该带账户号
		{Action: "TO_ORDER", OrderNo: "YH-EXC-0001", UserAccountNo: "9000000001", Note: "x"},
		{Action: "TO_BALANCE", UserAccountNo: "9000000001"}, // 缺说明
		{Action: "TO_BALANCE", UserAccountNo: "9000000001", Note: strings.Repeat("x", 501)},
	} {
		_, err := f.request(t, 0, bad)
		approvalCode(t, err, httpx.ErrBadRequest)
	}
	// 不存在的订单/账户：40401，而不是等到执行时才发现。
	_, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_ORDER",
		OrderNo: "YH-NOT-THERE", Note: "试试"})
	approvalCode(t, err, httpx.ErrNotFound)
	_, err = f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "0000000000", Note: "试试"})
	approvalCode(t, err, httpx.ErrNotFound)
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_approvals`); n != 0 {
		t.Fatalf("被拒绝的申请留下了 %d 张审批单", n)
	}
}

// TestExceptionResolveRefusesStaleApprovalIntegration：审批单可以在队列里放一天。
// 这一天里转账被自动结算认领走、或金额变了，执行必须拒绝而不是将错就错。
func TestExceptionResolveRefusesStaleApprovalIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before := f.balanceOf(t, f.payer)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "核对无误"})
	if err != nil {
		t.Fatal(err)
	}
	// 审批期间转账被自动结算认领走。
	f.write(t, `UPDATE chain_transfers SET match_status='MATCHED' WHERE id=$1`, f.transferID)
	_, err = f.approve(t, a)
	approvalCode(t, err, httpx.ErrApprovalStale)
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("已被认领的转账还是被记了一次")
	}
	if status, _, _ := f.exceptionState(t); status != "OPEN" {
		t.Fatal("执行失败却把异常单结掉了", status)
	}

	// 恢复，改成金额变了。
	f.write(t, `UPDATE chain_transfers SET match_status='EXCEPTION',amount_micro=$2 WHERE id=$1`,
		f.transferID, f.amountMicro+1)
	_, err = f.approve(t, a)
	approvalCode(t, err, httpx.ErrApprovalStale)
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("金额对不上还是记了一次")
	}
}

// TestExceptionResolveRefusesAlreadyResolvedAtExecutionIntegration：第二道防线。
//
// 发起时 buildPayload 会拦下已处置的异常，但那是**发起那一刻**的判断。审批单可以在
// 队列里放一天，这一天里这笔异常完全可能被另一条路结掉（并发处置、人工改库、
// 将来的其他入口）。执行那一刻必须自己再判一次——否则同一笔钱会被记两次。
//
// 这条用例专门绕开发起时的检查：先建审批单，再把异常单改成已处置，然后才审批。
func TestExceptionResolveRefusesAlreadyResolvedAtExecutionIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before := f.balanceOf(t, f.payer)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "核对无误"})
	if err != nil {
		t.Fatal(err)
	}
	// 审批期间这笔异常被另一条路结掉了。
	f.write(t, `UPDATE deposit_exceptions SET status='REJECTED',resolved_at=$2,approval_id=$3
 WHERE id=$1`, f.exceptionID, f.clock.Now(), 1)
	if _, err = f.approve(t, a); err == nil {
		t.Fatal("已处置的异常在执行时仍被再处置了一次")
	}
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("同一笔钱被记了第二次", before, got)
	}
	if status, _, _ := f.exceptionState(t); status != "REJECTED" {
		t.Fatal("执行失败却改了异常单状态", status)
	}
}

// TestExceptionResolveRefusesOrderAlreadyPaidIntegration：一张订单只能收一笔钱。
// 目标订单已经关联了链上转账，就绝不能再把第二笔记上去——那是重复入账。
// 发起时与执行时都要挡：这里分别走两条路各测一次。
func TestExceptionResolveRefusesOrderAlreadyPaidIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	// 发起时就该挡下来。
	f.write(t, `UPDATE orders SET chain_transfer_id=$2 WHERE id=$1`, f.orderID, f.transferID)
	_, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_ORDER",
		OrderNo: f.orderNo, Note: "试试"})
	approvalCode(t, err, httpx.ErrOrderState)

	// 执行时也该挡：先在订单干净时建单，再让订单被别的转账占掉。
	f.write(t, `UPDATE orders SET chain_transfer_id=NULL WHERE id=$1`, f.orderID)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_ORDER",
		OrderNo: f.orderNo, Note: "少付 0.5，按原单放行"})
	if err != nil {
		t.Fatal(err)
	}
	var other int64
	if err = f.owner.QueryRow(f.ctx, `INSERT INTO chain_transfers(network,tx_hash,event_index,
 contract_address,from_address,to_address,receive_address_id,amount_micro,block_number,block_time,
 confirm_status,match_status,data_source,created_at)
 VALUES('MOCK','tx-other',0,'MOCK','PAYER2','MOCKADDR-EXC',1,$1,2,$2,'SOLIDIFIED','MATCHED','MOCK',$2)
 RETURNING id`, f.payMicro, f.clock.Now()).Scan(&other); err != nil {
		t.Fatal(err)
	}
	// 只挂上别的转账，状态仍留在可接收的 PENDING——这样挡住它的只能是
	// 「订单已关联转账」这一条，而不是顺带被状态检查挡下。
	// orders.chain_transfer_id 是唯一索引，改写它等于把另一笔转账的归属悄悄抹掉。
	f.write(t, `UPDATE orders SET chain_transfer_id=$2 WHERE id=$1`, f.orderID, other)
	before := f.balanceOf(t, f.payer)
	if _, err = f.approve(t, a); err == nil {
		t.Fatal("已经收过钱的订单又被记了一笔")
	}
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("重复入账发生了", before, got)
	}
	if status, _, _ := f.exceptionState(t); status != "OPEN" {
		t.Fatal("执行失败却结掉了异常单", status)
	}
}

// TestExceptionResolveRefusesTamperedApprovalIntegration：审批单的目标与内容必须自洽。
// 把 target_id 改成另一笔异常，而 payload 与 payload_hash 原封不动——
// 这正是「审批人看的是 A，执行的是 B」那种最坏情况。执行必须拒绝。
func TestExceptionResolveRefusesTamperedApprovalIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	first := f.exceptionID
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "核对无误"})
	if err != nil {
		t.Fatal(err)
	}
	// 另造一笔异常，把审批单的目标偷偷换过去。
	f.seedException(t, "YH-EXC-0002")
	second := f.exceptionID
	if first == second {
		t.Fatal("夹具没有造出第二笔异常")
	}
	f.write(t, `UPDATE admin_approvals SET target_id=$2 WHERE id=$1`, a.ID, second)
	before := f.balanceOf(t, f.payer)
	_, err = f.approve(t, a)
	approvalCode(t, err, httpx.ErrApprovalStale)
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("被改过目标的审批单还是动了钱")
	}
	for _, id := range []int64{first, second} {
		var status string
		if err = f.pool.QueryRow(f.ctx, `SELECT status FROM deposit_exceptions WHERE id=$1`, id).
			Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "OPEN" {
			t.Fatalf("异常 %d 被处置了：%s", id, status)
		}
	}

	// 另一种改法：目标不动，只改 payload 本身而不改 payload_hash。
	// 审批人点头的是「转入余额」，执行的却可能是「转入订单」——
	// 执行器重算一遍校验和就是为了挡这一刀。
	f.write(t, `UPDATE admin_approvals SET target_id=$2 WHERE id=$1`, a.ID, first)
	var payload []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT payload FROM admin_approvals WHERE id=$1`, a.ID).
		Scan(&payload); err != nil {
		t.Fatal(err)
	}
	// payload 列是 jsonb，PostgreSQL 会按自己的规则重排键并去掉空白，
	// 所以这里按解析后的对象改，而不是对原文做字符串替换。
	var fields map[string]any
	if err = json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["action"] != "TO_BALANCE" {
		t.Fatal("payload 里没有预期的 action 字段，用例失效", fields)
	}
	fields["action"], fields["order_no"] = "TO_ORDER", f.orderNo
	delete(fields, "user_account_no")
	tampered, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, `UPDATE admin_approvals SET payload=$2 WHERE id=$1`, a.ID, tampered)
	_, err = f.approve(t, a)
	approvalCode(t, err, httpx.ErrApprovalStale)
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("被改过内容的审批单还是动了钱")
	}
	var orderStatus string
	if err = f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, f.orderID).
		Scan(&orderStatus); err != nil {
		t.Fatal(err)
	}
	if orderStatus != "PENDING" {
		t.Fatal("被改过内容的审批单结算了订单", orderStatus)
	}
}

// TestExceptionResolveRequiresTwoPeopleIntegration：发起人不能审批自己的申请，
// 而且没有权限的人既发不起也批不了。
func TestExceptionResolveRequiresTwoPeopleIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	before := f.balanceOf(t, f.payer)
	a, err := f.request(t, 0, ExceptionResolveInput{Action: "TO_BALANCE",
		UserAccountNo: "9000000001", Note: "核对无误"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(a.ID)
	own := f.proof(t, 0, approvalDecision, "/admin/v1/approvals/"+a.ID+"/approve", `{}`)
	_, err = f.svc.Decide(f.ctx, own, id, true, "")
	approvalCode(t, err, httpx.ErrApproverIsAuthor)
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("自批成功了")
	}
	// 撤掉能力之后，审批人也不能执行——执行器要求双方都仍然持有这个动作的权限。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code=$2`,
		f.role, actionExceptionResolve)
	_, err = f.approve(t, a)
	if err == nil {
		t.Fatal("发起人已被撤权，审批仍然执行了")
	}
	if got := f.balanceOf(t, f.payer); got != before {
		t.Fatal("撤权后还是动了钱")
	}
}

// TestExceptionResolveOneOpenApprovalPerExceptionIntegration：同一笔异常同时只允许
// 一张待审批单——同一笔钱上挂两张互相矛盾的申请是最危险的状态。
func TestExceptionResolveOneOpenApprovalPerExceptionIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	in := ExceptionResolveInput{Action: "TO_BALANCE", UserAccountNo: "9000000001", Note: "核对无误"}
	first, err := f.request(t, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	// 同一个人重复提交同一件事：复用那张单，不堆第二张。
	again, err := f.request(t, 0, in)
	if err != nil || again.ID != first.ID {
		t.Fatal("相同申请没有复用待审批单", again, err)
	}
	// 换一种处置方式：不该被当成重复提交而悄悄复用，也不该并存两张。
	_, err = f.request(t, 0, ExceptionResolveInput{Action: "REJECT", Note: "改主意了"})
	approvalCode(t, err, httpx.ErrConflict)
	// 换个人提交同一件事：同样冲突。
	_, err = f.request(t, 1, in)
	approvalCode(t, err, httpx.ErrConflict)
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_approvals WHERE status='PENDING'`); n != 1 {
		t.Fatalf("同一笔异常上挂了 %d 张待审批单", n)
	}
}

// TestDepositExceptionStatusFilterMatchesDatabaseIntegration：只读列表的状态筛选。
// 契约原先写的是 OPEN / RESOLVED，而库里从来没有 RESOLVED 这个值——
// 按它筛永远返回零条且不报错。这条用例钉住改正后的四个值。
func TestDepositExceptionStatusFilterMatchesDatabaseIntegration(t *testing.T) {
	f := newExceptionFixture(t)
	p := f.people[0]
	open, err := f.read.DepositExceptions(f.ctx, p, FinanceFilter{Status: "OPEN"})
	if err != nil || open.Total != "1" {
		t.Fatal("OPEN 筛选", open, err)
	}
	// 数据库里根本不存在的那个值必须被拒绝，而不是安静地返回空列表。
	_, err = f.read.DepositExceptions(f.ctx, p, FinanceFilter{Status: "RESOLVED"})
	approvalCode(t, err, httpx.ErrBadRequest)

	a, err := f.request(t, 0, ExceptionResolveInput{Action: "REJECT", Note: "投毒地址"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.approve(t, a); err != nil {
		t.Fatal(err)
	}
	rejected, err := f.read.DepositExceptions(f.ctx, p, FinanceFilter{Status: "REJECTED"})
	if err != nil || rejected.Total != "1" || rejected.Items[0].Status != "REJECTED" {
		t.Fatal("REJECTED 筛选不出已驳回的异常", rejected, err)
	}
	if rejected.Items[0].ResolutionNote == nil || *rejected.Items[0].ResolutionNote != "投毒地址" {
		t.Fatal("处置说明没有回到列表里", rejected.Items[0].ResolutionNote)
	}
	still, err := f.read.DepositExceptions(f.ctx, p, FinanceFilter{Status: "OPEN"})
	if err != nil || still.Total != "0" {
		t.Fatal("处置过的异常还挂在 OPEN 上", still, err)
	}
}
