package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/egress"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 出口管理的测试重点：权限单独授予、凭据只进不出（响应/审计/幂等记录里都没有）、
// 字段加密且绑定行、预览不落库、提交幂等、订阅刷新的停用语义与抓取失败不动数据。

type fakeFetcher struct {
	body string
	err  error
	urls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, raw string) (string, error) {
	f.urls = append(f.urls, raw)
	return f.body, f.err
}

type egressFixture struct {
	*approvalFixture
	svc    *EgressService
	fetch  *fakeFetcher
	cipher *fieldenc.Cipher
}

func newEgressFixture(t *testing.T) *egressFixture {
	t.Helper()
	a := newApprovalFixture(t)
	before := authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00041_egress_endpoints.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	if _, err := a.owner.Exec(a.ctx, up); err != nil {
		t.Fatal(err)
	}
	// 00041 建表时还没有人数上限。集成测试不跑完整 goose，这里补上 00046 的那一列。
	if _, err := a.owner.Exec(a.ctx, `
		ALTER TABLE egress_endpoints
		  ADD COLUMN capacity_users INT NOT NULL DEFAULT 0
		  CHECK (capacity_users BETWEEN 0 AND 1000000)`); err != nil {
		t.Fatal(err)
	}
	// 迁移只给 super_admin 角色授权；fixture 的角色不是它，因此不得增加授权。
	if authCount(t, a.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移给非 super_admin 角色授了权")
	}
	role := pgx.Identifier{a.pool.Config().ConnConfig.User}.Sanitize()
	for _, sql := range []string{
		"GRANT SELECT,INSERT,UPDATE ON " + pgx.Identifier{a.schema, "egress_endpoints"}.Sanitize() + "," +
			pgx.Identifier{a.schema, "egress_subscriptions"}.Sanitize() + " TO " + role,
		"GRANT SELECT,INSERT ON " + pgx.Identifier{a.schema, "admin_egress_mutations"}.Sanitize() + " TO " + role,
		"GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + pgx.Identifier{a.schema}.Sanitize() + " TO " + role,
	} {
		if _, err := a.owner.Exec(a.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	c, err := fieldenc.New("fek-test", []byte(strings.Repeat("q", 32)))
	if err != nil {
		t.Fatal(err)
	}
	ff := &fakeFetcher{}
	return &egressFixture{approvalFixture: a, fetch: ff, cipher: c,
		svc: &EgressService{Auth: a.svc.Auth, Audit: a.audit, Cipher: c, Fetcher: ff}}
}

func (f *egressFixture) grant(t *testing.T, codes ...string) {
	t.Helper()
	for _, code := range codes {
		f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,$2)`, f.role, code)
	}
}

func (f *egressFixture) proofHash(seed string) ApprovalProof {
	return ApprovalProof{Principal: f.people[0], RequestHash: hash64(seed), RequestID: strings.Repeat("r", 16), IP: "203.0.113.9", UserAgent: "fixture"}
}

func egressCode(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var coded *httpx.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("期望错误码 %d，实际 %v", want, err)
	}
}

const egressSample = `203.0.113.20:1080:alice:ALICEPASSWORD
203.0.113.21:1080:bob:BOBPASSWORD
203.0.113.20:1080:alice:ALICEPASSWORD
trojan://TROJANPASSWORD@t.example.com:443?allowInsecure=1
tuic://0b5c1f6e-2d1c-4e8b-9a8b-1c2d3e4f5a6b:x@h.example.com:443
not a proxy line`

var egressSecrets = []string{"ALICEPASSWORD", "BOBPASSWORD", "TROJANPASSWORD", "alice", "bob"}

func noSecrets(t *testing.T, label string, v any) {
	t.Helper()
	blob, _ := json.Marshal(v)
	for _, s := range egressSecrets {
		if strings.Contains(string(blob), s) {
			t.Fatalf("%s 泄露了 %q：%s", label, s, blob)
		}
	}
}

func TestEgressRequiresItsOwnPermissionsIntegration(t *testing.T) {
	f := newEgressFixture(t)
	p := f.people[0]
	_, err := f.svc.List(f.ctx, p, EgressFilter{Page: 1, PageSize: 20})
	egressCode(t, err, httpx.ErrForbidden)
	_, err = f.svc.Preview(f.ctx, p, EgressImportInput{Text: egressSample})
	egressCode(t, err, httpx.ErrForbidden)
	_, err = f.svc.Import(f.ctx, f.proofHash("a"), EgressImportInput{OperationID: uuid.NewString(), Text: egressSample})
	egressCode(t, err, httpx.ErrForbidden)

	// 只读不能写。
	f.grant(t, "egress.read")
	if _, err = f.svc.List(f.ctx, p, EgressFilter{Page: 1, PageSize: 20}); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Preview(f.ctx, p, EgressImportInput{Text: egressSample})
	egressCode(t, err, httpx.ErrForbidden)
}

func TestEgressPreviewImportAndEncryptionIntegration(t *testing.T) {
	f := newEgressFixture(t)
	f.grant(t, "egress.read", "egress.write")
	p := f.people[0]

	pre, err := f.svc.Preview(f.ctx, p, EgressImportInput{Text: egressSample, CountryCode: "jp"})
	if err != nil {
		t.Fatal(err)
	}
	if pre.Summary.Total != 6 || pre.Summary.Valid != 2 || pre.Summary.Duplicate != 1 || pre.Summary.Invalid != 3 || pre.Summary.Existing != 0 {
		t.Fatalf("预览汇总不对：%+v", pre.Summary)
	}
	noSecrets(t, "预览", pre)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM egress_endpoints`) != 0 {
		t.Fatal("预览落了库")
	}

	op := uuid.NewString()
	in := EgressImportInput{OperationID: op, Text: egressSample, CountryCode: "JP", Enable: true}
	first, err := f.svc.Import(f.ctx, f.proofHash("imp"), in)
	if err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "导入结果", json.RawMessage(first))
	var res egress.ImportResult
	if err := json.Unmarshal(first, &res); err != nil || res.Created != 2 || res.Failed != 3 || res.Skipped != 1 {
		t.Fatalf("导入结果 %s %v", first, err)
	}
	// 幂等：同键同体返回逐字节相同的结果，不重复建。
	second, err := f.svc.Import(f.ctx, f.proofHash("imp"), in)
	if err != nil || string(first) != string(second) {
		t.Fatalf("幂等失败：%v", err)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM egress_endpoints`) != 2 {
		t.Fatal("重复提交建出了多余的出口")
	}
	// 同键换体：40901。
	_, err = f.svc.Import(f.ctx, f.proofHash("other"), in)
	egressCode(t, err, httpx.ErrConflict)

	// 凭据只以密文存在，且绑定行（换行解密失败）。
	var id1, id2 int64
	var blob1, blob2 []byte
	if err := f.owner.QueryRow(f.ctx, `SELECT id,secret_enc FROM egress_endpoints WHERE host='203.0.113.20'`).Scan(&id1, &blob1); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.QueryRow(f.ctx, `SELECT id,secret_enc FROM egress_endpoints WHERE host='203.0.113.21'`).Scan(&id2, &blob2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob1), "ALICEPASSWORD") {
		t.Fatal("密码以明文落库")
	}
	sec, err := egress.OpenSecret(f.cipher, id1, blob1)
	if err != nil || sec["pass"] != "ALICEPASSWORD" || sec["user"] != "alice" {
		t.Fatalf("解密失败 %v", err)
	}
	if _, err := egress.OpenSecret(f.cipher, id2, blob1); err == nil {
		t.Fatal("密文可以被搬到别的行")
	}
	// 审计与幂等记录里都没有凭据。
	var auditBlob, mutBlob string
	_ = f.owner.QueryRow(f.ctx, `SELECT COALESCE(string_agg(COALESCE(after_data::text,'')||COALESCE(before_data::text,''),''),'') FROM admin_audit_logs`).Scan(&auditBlob)
	_ = f.owner.QueryRow(f.ctx, `SELECT string_agg(response::text,'') FROM admin_egress_mutations`).Scan(&mutBlob)
	for _, s := range egressSecrets {
		if strings.Contains(auditBlob, s) || strings.Contains(mutBlob, s) {
			t.Fatalf("审计或幂等记录泄露了 %q", s)
		}
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_audit_logs WHERE action_code='egress.import'`) != 1 {
		t.Fatal("导入没有审计记录")
	}

	// 列表：脱敏。
	list, err := f.svc.List(f.ctx, p, EgressFilter{Page: 1, PageSize: 20, CountryCode: "jp"})
	if err != nil || list.Total != "2" || list.Items[0].IdentityHint != "al***" || list.Items[0].Status != "ENABLED" {
		t.Fatalf("列表 %+v %v", list, err)
	}
	noSecrets(t, "列表", list)

	// 再次预览：标出已存在。
	pre, err = f.svc.Preview(f.ctx, p, EgressImportInput{Text: "203.0.113.20:1080:alice:NEWPASS"})
	if err != nil || pre.Summary.Existing != 1 || pre.Rows[0].ExistingID == "" {
		t.Fatalf("已存在检测 %+v %v", pre, err)
	}
	// 默认跳过已存在；update_existing 时更新密码。
	out, err := f.svc.Import(f.ctx, f.proofHash("upd"), EgressImportInput{OperationID: uuid.NewString(), Text: "203.0.113.20:1080:alice:NEWPASS", UpdateExisting: true})
	if err != nil || !strings.Contains(string(out), `"updated":1`) {
		t.Fatalf("更新导入 %s %v", out, err)
	}
	_ = f.owner.QueryRow(f.ctx, `SELECT secret_enc FROM egress_endpoints WHERE id=$1`, id1).Scan(&blob1)
	if sec, _ := egress.OpenSecret(f.cipher, id1, blob1); sec["pass"] != "NEWPASS" {
		t.Fatal("update_existing 没有更新凭据")
	}
}

func TestEgressUpdateIntegration(t *testing.T) {
	f := newEgressFixture(t)
	f.grant(t, "egress.read", "egress.write")
	if _, err := f.svc.Import(f.ctx, f.proofHash("i"), EgressImportInput{OperationID: uuid.NewString(),
		Text: "http://u:p@203.0.113.30:3128\nsocks5://u:p@203.0.113.31:1080"}); err != nil {
		t.Fatal(err)
	}
	var httpID, socksID int64
	_ = f.owner.QueryRow(f.ctx, `SELECT id FROM egress_endpoints WHERE protocol='HTTP'`).Scan(&httpID)
	_ = f.owner.QueryRow(f.ctx, `SELECT id FROM egress_endpoints WHERE protocol='SOCKS5'`).Scan(&socksID)
	on, udp, tier := "ENABLED", true, 3
	name := "首尔住宅 01"
	out, err := f.svc.Update(f.ctx, f.proofHash("u1"), "e"+itoa(socksID), EgressUpdate{OperationID: uuid.NewString(), Status: &on, SupportsUDP: &udp, MinTier: &tier, Name: &name})
	if err != nil || !strings.Contains(string(out), `"status":"ENABLED"`) || !strings.Contains(string(out), `"supports_udp":true`) {
		t.Fatalf("修改 %s %v", out, err)
	}
	// HTTP 不能标 UDP；库里的 CHECK 也兜底。
	_, err = f.svc.Update(f.ctx, f.proofHash("u2"), "e"+itoa(httpID), EgressUpdate{OperationID: uuid.NewString(), SupportsUDP: &udp})
	egressCode(t, err, httpx.ErrBadRequest)
	if _, err := f.owner.Exec(f.ctx, `UPDATE egress_endpoints SET supports_udp=true WHERE id=$1`, httpID); err == nil {
		t.Fatal("库约束没有挡住 HTTP 标 UDP")
	}
	bad := "PAUSED"
	_, err = f.svc.Update(f.ctx, f.proofHash("u3"), "e"+itoa(httpID), EgressUpdate{OperationID: uuid.NewString(), Status: &bad})
	egressCode(t, err, httpx.ErrBadRequest)
	_, err = f.svc.Update(f.ctx, f.proofHash("u4"), "e999999", EgressUpdate{OperationID: uuid.NewString(), Status: &on})
	egressCode(t, err, httpx.ErrNotFound)
	_, err = f.svc.Update(f.ctx, f.proofHash("u5"), "12", EgressUpdate{OperationID: uuid.NewString(), Status: &on})
	egressCode(t, err, httpx.ErrBadRequest)
}

func TestEgressSubscriptionLifecycleIntegration(t *testing.T) {
	f := newEgressFixture(t)
	f.grant(t, "egress.read", "egress.write")
	p := f.people[0]

	// 内网地址在任何网络请求之前就被拒绝。
	_, err := f.svc.CreateSubscription(f.ctx, f.proofHash("s0"), SubscriptionInput{OperationID: uuid.NewString(), Name: "坏", URL: "https://127.0.0.1/sub"})
	egressCode(t, err, httpx.ErrBadRequest)
	_, err = f.svc.SubscriptionPreview(f.ctx, p, SubscriptionInput{URL: "https://10.0.0.1/sub"})
	egressCode(t, err, httpx.ErrBadRequest)
	if len(f.fetch.urls) != 0 {
		t.Fatal("内网地址仍然发起了抓取")
	}

	secretURL := "https://sub.example.com/api/v1/client?token=SUBTOKEN123"
	out, err := f.svc.CreateSubscription(f.ctx, f.proofHash("s1"), SubscriptionInput{OperationID: uuid.NewString(), Name: "供应商A", URL: secretURL, CountryCode: "sg"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SUBTOKEN123") {
		t.Fatal("订阅响应泄露了令牌")
	}
	subs, err := f.svc.Subscriptions(f.ctx, p)
	if err != nil || len(subs) != 1 || subs[0].URLHint != "sub.example.com" {
		t.Fatalf("订阅列表 %+v %v", subs, err)
	}
	noSecrets(t, "订阅列表", subs)
	if blob, _ := json.Marshal(subs); strings.Contains(string(blob), "SUBTOKEN123") {
		t.Fatal("订阅列表泄露了令牌")
	}

	f.fetch.body = "socks5://a:ALICEPASSWORD@203.0.113.40:1080\nsocks5://b:BOBPASSWORD@203.0.113.41:1080\n"
	out, err = f.svc.RefreshSubscription(f.ctx, f.proofHash("r1"), subs[0].ID, SubscriptionRefresh{OperationID: uuid.NewString(), Enable: true})
	if err != nil || !strings.Contains(string(out), `"created":2`) {
		t.Fatalf("刷新 %s %v", out, err)
	}
	noSecrets(t, "刷新结果", json.RawMessage(out))
	if f.fetch.urls[len(f.fetch.urls)-1] != secretURL {
		t.Fatal("没有用解密后的订阅地址抓取")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM egress_endpoints WHERE source='SUBSCRIPTION' AND status='ENABLED' AND country_code='SG'`) != 2 {
		t.Fatal("订阅条目没有按默认值落库")
	}

	// 下一次订阅里少了一条：停用而不是删除。
	f.fetch.body = "socks5://a:ALICEPASSWORD@203.0.113.40:1080\n"
	out, err = f.svc.RefreshSubscription(f.ctx, f.proofHash("r2"), subs[0].ID, SubscriptionRefresh{OperationID: uuid.NewString(), Enable: true})
	if err != nil || !strings.Contains(string(out), `"disabled":1`) {
		t.Fatalf("二次刷新 %s %v", out, err)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM egress_endpoints WHERE host='203.0.113.41' AND status='DISABLED'`) != 1 {
		t.Fatal("消失的条目没有被停用")
	}

	// 抓取失败：记下失败，不动任何出口。
	f.fetch.err = errors.New("订阅抓取失败（网络错误或超时）")
	out, err = f.svc.RefreshSubscription(f.ctx, f.proofHash("r3"), subs[0].ID, SubscriptionRefresh{OperationID: uuid.NewString()})
	if err != nil || !strings.Contains(string(out), `"ok":false`) {
		t.Fatalf("失败刷新 %s %v", out, err)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM egress_endpoints WHERE host='203.0.113.40' AND status='ENABLED'`) != 1 {
		t.Fatal("抓取失败改动了已有出口")
	}
	subs, _ = f.svc.Subscriptions(f.ctx, p)
	if !strings.HasPrefix(subs[0].LastResult, "失败") {
		t.Fatalf("没有记录失败：%+v", subs[0])
	}
}

func TestEgressMigrationGrantsSuperAdminIntegration(t *testing.T) {
	f := newEgressFixture(t)
	var role int64
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO admin_roles(code,name) VALUES('super_admin','超级管理员') RETURNING id`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../db/migrations/00041_egress_endpoints.sql")
	up := strings.Split(string(raw), "-- +goose Down")[0]
	// 只重放授权那一段（表已存在）。
	i := strings.Index(up, "INSERT INTO admin_role_permissions")
	j := strings.Index(up, "CREATE TABLE egress_subscriptions")
	if _, err := f.owner.Exec(f.ctx, up[i:j]); err != nil {
		t.Fatal(err)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions WHERE role_id=`+itoa(role)+` AND permission_code LIKE 'egress.%'`) != 2 {
		t.Fatal("super_admin 没有拿到出口权限")
	}
}

func itoa(v int64) string { return strings.TrimSpace(strings.Replace(jsonNumber(v), `"`, "", -1)) }

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
