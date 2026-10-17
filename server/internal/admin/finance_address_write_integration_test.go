package admin

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 这两个写操作的测试重点，是它们各自那条「改错了会影响线上」的链路：
//
//   - 收款地址停用只影响新订单的分配，但停掉最后一个可用地址会让下单直接失败，
//     必须要求显式确认；启用一个已过期的地址是假象，必须拒绝。
//   - 公共付款地址只影响邀请风控；本系统自己的收款地址不能被标成公共付款地址。

type financeAddrFixture struct {
	*managedUserFixture
	writer    *FinanceAddressService
	reader    *FinanceReadService
	addrA     int64
	addrB     int64
	expiredID int64
}

func newFinanceAddrFixture(t *testing.T) *financeAddrFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"receive_addresses", "shared_pay_addresses"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	// The fixture clones the latest public schema, so restore the pre-00033 shape
	// before testing that migration itself. This only touches the random test schema.
	f.write(t, "ALTER TABLE "+pgx.Identifier{f.schema, "shared_pay_addresses"}.Sanitize()+" DROP COLUMN IF EXISTS id")
	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	for _, file := range []string{"00026_admin_finance_read.sql", "00033_admin_finance_address_write.sql"} {
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
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code IN ('finance.address_status','finance.shared_pay_write') AND risk_level=2`) != 2 {
		t.Fatal("两个写能力未定义或风险等级不对")
	}

	f.write(t, "GRANT SELECT,UPDATE ON "+pgx.Identifier{f.schema, "receive_addresses"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{f.schema, "shared_pay_addresses"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{f.schema, "admin_finance_mutations"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'finance.read'),($1,'finance.address_status'),($1,'finance.shared_pay_write')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, financeRead,
			actionAddressStatus, actionSharedPay)
	}

	out := &financeAddrFixture{managedUserFixture: f,
		writer: &FinanceAddressService{Security: f.svc},
		reader: &FinanceReadService{Auth: f.svc.Auth}}
	future := f.clock.Now().Add(365 * 24 * time.Hour)
	past := f.clock.Now().Add(-time.Hour)
	for _, spec := range []struct {
		addr   string
		status string
		until  time.Time
		into   *int64
	}{
		{"MOCKADDR-A", "ACTIVE", future, &out.addrA},
		{"MOCKADDR-B", "ACTIVE", future, &out.addrB},
		{"MOCKADDR-EXPIRED", "PAUSED", past, &out.expiredID},
	} {
		if err := f.owner.QueryRow(f.ctx, `INSERT INTO receive_addresses(network,address,signature,
 sig_key_id,valid_until,status) VALUES('MOCK',$1,'sig','k1',$2,$3) RETURNING id`,
			spec.addr, spec.until, spec.status).Scan(spec.into); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f *financeAddrFixture) statusProof(t *testing.T, id int64, in ReceiveAddressStatusInput) ApprovalProof {
	t.Helper()
	path := "/admin/v1/receive-addresses/" + strconv.FormatInt(id, 10) + "/status"
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, actionAddressStatus, path, string(raw))
	p.RequestHash = RequestHash("POST", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func (f *financeAddrFixture) setStatus(t *testing.T, id int64, in ReceiveAddressStatusInput) (
	ManagedReceiveAddressStatus, error) {
	t.Helper()
	if in.OperationID == "" {
		in.OperationID = uuid.NewString()
	}
	if in.Reason == "" {
		in.Reason = "维护"
	}
	return f.writer.SetReceiveAddressStatus(f.ctx, f.statusProof(t, id, in), id, in)
}

func (f *financeAddrFixture) sharedProof(t *testing.T, in SharedPayAddressInput) ApprovalProof {
	t.Helper()
	path := "/admin/v1/shared-pay-addresses"
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, actionSharedPay, path, string(raw))
	p.RequestHash = RequestHash("POST", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func (f *financeAddrFixture) addShared(t *testing.T, in SharedPayAddressInput) (
	ManagedSharedPayAddress, error) {
	t.Helper()
	if in.OperationID == "" {
		in.OperationID = uuid.NewString()
	}
	if in.Reason == "" {
		in.Reason = "交易所热钱包"
	}
	return f.writer.AddSharedPayAddress(f.ctx, f.sharedProof(t, in), in)
}

func TestReceiveAddressStatusOnlyAffectsAllocationIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)

	out, err := f.setStatus(t, f.addrA, ReceiveAddressStatusInput{Status: "PAUSED"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "PAUSED" || out.PrevStatus != "ACTIVE" || out.ActiveRemaining != "1" {
		t.Fatal("状态与剩余可用数", out)
	}
	// 只改状态：地址本身、有效期、签名都不动，行也绝不删除。
	var count int
	var address, status string
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) OVER (),address,status FROM receive_addresses
 WHERE id=$1`, f.addrA).Scan(&count, &address, &status); err != nil {
		t.Fatal(err)
	}
	if address != "MOCKADDR-A" || status != "PAUSED" {
		t.Fatal("地址被改动或删除", address, status)
	}
	// RETIRED 也只是不再分配。
	retired, err := f.setStatus(t, f.addrA, ReceiveAddressStatusInput{Status: "RETIRED"})
	if err != nil || retired.Status != "RETIRED" || retired.ActiveRemaining != "1" {
		t.Fatal("退役", retired, err)
	}
	// 重新启用：有效期没过，放行。
	back, err := f.setStatus(t, f.addrA, ReceiveAddressStatusInput{Status: "ACTIVE"})
	if err != nil || back.Status != "ACTIVE" || back.ActiveRemaining != "2" {
		t.Fatal("重新启用", back, err)
	}
}

func TestReceiveAddressStatusGuardsTheLastUsableAddressIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)
	// 先停掉一个，还剩一个可用。
	if _, err := f.setStatus(t, f.addrA, ReceiveAddressStatusInput{Status: "PAUSED"}); err != nil {
		t.Fatal(err)
	}
	// 再停最后一个：必须被挡住，且**整笔回滚**——那个地址还得是 ACTIVE。
	_, err := f.setStatus(t, f.addrB, ReceiveAddressStatusInput{Status: "PAUSED"})
	nodeCode(t, err, httpx.ErrConflict)
	var status string
	if err = f.owner.QueryRow(f.ctx, `SELECT status FROM receive_addresses WHERE id=$1`,
		f.addrB).Scan(&status); err != nil || status != "ACTIVE" {
		t.Fatal("被拒的请求没有回滚", status, err)
	}
	// 带上显式确认就能停：全站停收款是正当操作，只是不该悄悄发生。
	out, err := f.setStatus(t, f.addrB, ReceiveAddressStatusInput{Status: "PAUSED", ConfirmLast: true})
	if err != nil || out.ActiveRemaining != "0" {
		t.Fatal("显式确认后应当放行", out, err)
	}
}

func TestReceiveAddressStatusRejectsExpiredAndBadInputIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)
	// 启用一个签名有效期已过的地址：pickAddress 会立刻跳过它，启用成功只是假象。
	_, err := f.setStatus(t, f.expiredID, ReceiveAddressStatusInput{Status: "ACTIVE"})
	nodeCode(t, err, httpx.ErrBadRequest)
	// 但把它改成 RETIRED 是可以的——那不需要它可用。
	if _, err = f.setStatus(t, f.expiredID, ReceiveAddressStatusInput{Status: "RETIRED"}); err != nil {
		t.Fatal("过期地址应当可以退役", err)
	}
	for name, in := range map[string]ReceiveAddressStatusInput{
		"状态非法":  {Status: "DELETED"},
		"状态大小写": {Status: "active"},
		"状态为空":  {Status: ""},
	} {
		_, err = f.setStatus(t, f.addrA, in)
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 不存在的地址：404 而不是静默成功。
	_, err = f.setStatus(t, 999999, ReceiveAddressStatusInput{Status: "PAUSED"})
	nodeCode(t, err, httpx.ErrNotFound)
}

func TestSharedPayAddressRegistrationIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)

	out, err := f.addShared(t, SharedPayAddressInput{Address: "TExchangeHotWallet1", Note: "某交易所"})
	if err != nil {
		t.Fatal(err)
	}
	// 人工登记必须与扫描自动识别分得开。
	if out.AutoDetected || out.Note == nil || *out.Note != "某交易所" {
		t.Fatal("人工登记应当 auto_detected=false", out)
	}
	// 重复登记：40901。
	_, err = f.addShared(t, SharedPayAddressInput{Address: "TExchangeHotWallet1"})
	nodeCode(t, err, httpx.ErrConflict)

	// 本系统自己的收款地址不能被标成公共付款地址。
	_, err = f.addShared(t, SharedPayAddressInput{Address: "MOCKADDR-A"})
	nodeCode(t, err, httpx.ErrBadRequest)
	var registered bool
	if err = f.owner.QueryRow(f.ctx, `SELECT EXISTS(SELECT 1 FROM shared_pay_addresses
 WHERE address='MOCKADDR-A')`).Scan(&registered); err != nil || registered {
		t.Fatal("被拒的地址进了库", registered, err)
	}

	for name, in := range map[string]SharedPayAddressInput{
		"地址为空":   {Address: ""},
		"地址过长":   {Address: strings.Repeat("T", 65)},
		"备注过长":   {Address: "TOther1", Note: strings.Repeat("x", 129)},
		"地址含控制符": {Address: "TOther\x002"},
	} {
		_, err = f.addShared(t, in)
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}

	// 列表沿用 finance.read，按登记时间倒序，自动识别的那条也在。
	f.write(t, `INSERT INTO shared_pay_addresses(address,note,auto_detected,created_at)
 VALUES('TAutoDetected1','扫描识别',true,$1)`, f.clock.Now().Add(-time.Hour))
	page, err := f.reader.SharedPayAddresses(f.ctx, f.people[0], FinanceFilter{})
	if err != nil || page.Total != "2" || len(page.Items) != 2 {
		t.Fatal("列表", page, err)
	}
	if page.Items[0].Address != "TExchangeHotWallet1" || !page.Items[1].AutoDetected {
		t.Fatal("排序或 auto_detected 投影不对", page.Items)
	}
}

func TestFinanceAddressWritesRequireOwnCapabilitiesIntegration(t *testing.T) {
	f := newFinanceAddrFixture(t)

	// 没有二次验证令牌。
	in := ReceiveAddressStatusInput{OperationID: uuid.NewString(), Reason: "维护", Status: "PAUSED"}
	p := f.statusProof(t, f.addrA, in)
	p.Token = ""
	_, err := f.writer.SetReceiveAddressStatus(f.ctx, p, f.addrA, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	// 两个能力互不替代：撤掉启停能力后仍能登记公共付款地址。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='finance.address_status'`, f.role)
	_, err = f.setStatus(t, f.addrA, ReceiveAddressStatusInput{Status: "PAUSED"})
	nodeCode(t, err, httpx.ErrForbidden)
	if _, err = f.addShared(t, SharedPayAddressInput{Address: "TStillAllowed1"}); err != nil {
		t.Fatal("另一个能力不该被牵连", err)
	}

	var status string
	if err = f.owner.QueryRow(f.ctx, `SELECT status FROM receive_addresses WHERE id=$1`,
		f.addrA).Scan(&status); err != nil || status != "ACTIVE" {
		t.Fatal("失败的请求改动了地址", status, err)
	}
}
