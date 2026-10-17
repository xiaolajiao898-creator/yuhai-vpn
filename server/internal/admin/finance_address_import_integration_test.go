package admin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 地址导入这条路要钉住的只有一句话：**服务端第一次真的验签了**，而且验不过就一条都不进库。
//
// 其余几条同样重要：
//   - 返回 202 时地址还没入库，真正写入发生在另一名管理员点通过时；
//   - 导入的地址一律是 PAUSED，不能一进来就开始收钱；
//   - 整包原子：包里有一个地址已存在，其余的也不许进去。

type importFixture struct {
	*approvalFixture
	svcImport *FinanceImportService
	priv      ed25519.PrivateKey
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	f := newApprovalFixture(t)
	// DDL 与授权走 owner（迁移角色）：应用角色在这个 schema 上没有建表权限，
	// 这本身就是铁律的一部分。
	own := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.owner.Exec(f.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	own("CREATE TABLE " + pgx.Identifier{f.schema, "receive_addresses"}.Sanitize() +
		" (LIKE " + pgx.Identifier{"public", "receive_addresses"}.Sanitize() + " INCLUDING ALL)")
	own("GRANT SELECT,INSERT,UPDATE ON " +
		pgx.Identifier{f.schema, "receive_addresses"}.Sanitize() + " TO " +
		pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize())
	own("GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " +
		pgx.Identifier{f.schema}.Sanitize() + " TO " +
		pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize())

	raw, err := os.ReadFile("../db/migrations/00034_admin_address_import.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	own(up)
	var defined int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_permissions
 WHERE code='finance.address_import' AND risk_level=3`).Scan(&defined); err != nil || defined != 1 {
		t.Fatal("导入能力未定义或风险等级不对", defined, err)
	}
	// 夹具的角色默认持有全部已定义能力；把新定义的这个也授上。
	f.exec(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'finance.address_import') ON CONFLICT DO NOTHING`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, actionAddressImport)
	}

	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	f.svc.FundPubKey = pub
	return &importFixture{approvalFixture: f, priv: priv,
		svcImport: &FinanceImportService{Security: f.svc, FundPubKey: pub}}
}

// sign 按 cmd/fundsign 的原文格式签一条地址。
func (f *importFixture) sign(network, address string, until time.Time) AddressImportEntry {
	msg := fmt.Sprintf("yuhai-addr-v1|%s|%s|%d", network, address, until.Unix())
	return AddressImportEntry{Network: network, Address: address, ValidUntil: until,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.priv, []byte(msg)))}
}

func (f *importFixture) pkg(entries ...AddressImportEntry) AddressImportPackage {
	return AddressImportPackage{KeyID: "fund-test-1", Addresses: entries}
}

func (f *importFixture) request(t *testing.T, person int, in AddressImportInput) (Approval, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	p := f.proof(t, person, actionAddressImport, "/admin/v1/receive-addresses/import", string(raw))
	return f.svcImport.RequestImport(f.ctx, p, in)
}

func (f *importFixture) decide(t *testing.T, person int, id int64, approve bool, note string) (Approval, error) {
	t.Helper()
	body := `{"approve":true}`
	if !approve {
		body = `{"approve":false,"note":"` + note + `"}`
	}
	p := f.proof(t, person, approvalDecision,
		fmt.Sprintf("/admin/v1/approvals/%d/decision", id), body)
	return f.svc.Decide(f.ctx, p, id, approve, note)
}

func TestAddressImportVerifiesSignatureAndLandsPausedIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(365 * 24 * time.Hour).UTC().Truncate(time.Second)
	in := AddressImportInput{Reason: "导入本月地址包",
		Package: f.pkg(f.sign("MOCK", "MOCKIMPORT1", until), f.sign("TRON", "TIMPORT2", until))}

	approval, err := f.request(t, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != "PENDING" || !approval.ExecutorSupported {
		t.Fatal("审批单状态或执行器支持标记不对", approval.Status, approval.ExecutorSupported)
	}
	// 关键：这时地址一条都还没进库。
	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM receive_addresses`).Scan(&count); err != nil ||
		count != 0 {
		t.Fatal("发起审批时就把地址写进去了", count, err)
	}

	id, _ := positiveApprovalID(approval.ID)
	done, err := f.decide(t, 1, id, true, "")
	if err != nil || done.Status != "EXECUTED" {
		t.Fatal("审批通过", done, err)
	}
	rows, err := f.owner.Query(f.ctx, `SELECT network,address,status,sig_key_id,approval_id
 FROM receive_addresses ORDER BY address`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var network, address, status, keyID string
		var approvalID *int64
		if err = rows.Scan(&network, &address, &status, &keyID, &approvalID); err != nil {
			t.Fatal(err)
		}
		// 一律 PAUSED：刚导入的地址不该在没人看过之前就开始收钱。
		if status != "PAUSED" {
			t.Fatal("导入的地址不是 PAUSED", address, status)
		}
		if keyID != "fund-test-1" || approvalID == nil || *approvalID != id {
			t.Fatal("密钥标识或审批编号没记上", address, keyID, approvalID)
		}
		got = append(got, network+"/"+address)
	}
	if strings.Join(got, ",") != "MOCK/MOCKIMPORT1,TRON/TIMPORT2" {
		t.Fatal("入库内容不对", got)
	}
}

func TestAddressImportRejectsBadPackagesIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	good := f.sign("MOCK", "MOCKGOOD1", until)

	forged := good
	forged.Address = "MOCKFORGED1" // 签名对不上改过的地址
	tampered := good
	tampered.ValidUntil = until.Add(time.Hour) // 有效期被改，签名同样对不上
	badSig := good
	badSig.Signature = "not-base64url!!"
	badNetwork := f.sign("MOCK", "MOCKNET1", until)
	badNetwork.Network = "ETH"

	for name, pkg := range map[string]AddressImportPackage{
		"地址被改过":  f.pkg(forged),
		"有效期被改过": f.pkg(tampered),
		"签名格式非法": f.pkg(badSig),
		"网络非法":   f.pkg(badNetwork),
		"包内地址重复": f.pkg(good, good),
		"空包":     f.pkg(),
		"密钥标识为空": {KeyID: "", Addresses: []AddressImportEntry{good}},
	} {
		_, err := f.request(t, 0, AddressImportInput{Reason: "导入", Package: pkg})
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 一张审批单都不该建出来。
	var approvals int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_approvals`).Scan(&approvals); err != nil ||
		approvals != 0 {
		t.Fatal("被拒的包建出了审批单", approvals, err)
	}

	// 混包：一好一坏，整包拒绝。
	_, err := f.request(t, 0, AddressImportInput{Reason: "导入", Package: f.pkg(good, forged)})
	nodeCode(t, err, httpx.ErrBadRequest)
}

func TestAddressImportIsAtomicOnDuplicateIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	// 库里先有一个同名地址（模拟历史上手工插进去的那条）。
	f.exec(t, `INSERT INTO receive_addresses(network,address,signature,sig_key_id,valid_until,status)
 VALUES('MOCK','MOCKDUP1','old-sig','old-key',$1,'ACTIVE')`, until)

	in := AddressImportInput{Reason: "导入",
		Package: f.pkg(f.sign("MOCK", "MOCKNEW1", until), f.sign("MOCK", "MOCKDUP1", until))}
	approval, err := f.request(t, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(approval.ID)
	// 执行时才撞上唯一约束：整包回滚，新地址也不许留下。
	_, err = f.decide(t, 1, id, true, "")
	nodeCode(t, err, httpx.ErrConflict)
	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM receive_addresses`).Scan(&count); err != nil ||
		count != 1 {
		t.Fatal("半包导入了", count, err)
	}
	// 审批单仍是 PENDING——执行失败不该把它标成已执行。
	var status string
	if err = f.owner.QueryRow(f.ctx, `SELECT status FROM admin_approvals WHERE id=$1`, id).
		Scan(&status); err != nil || status != "PENDING" {
		t.Fatal("执行失败的审批单状态不对", status, err)
	}
}

func TestAddressImportNeedsFundPublicKeyIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	in := AddressImportInput{Reason: "导入", Package: f.pkg(f.sign("MOCK", "MOCKKEY1", until))}

	// 没配公钥就没有验签能力：宁可不能导入，也不要在这种状态下放行。
	f.svcImport.FundPubKey = ""
	_, err := f.request(t, 0, in)
	nodeCode(t, err, httpx.ErrMaintenance)

	// 执行阶段同理：审批单建好之后公钥没了，也不许执行。
	f.svcImport.FundPubKey = f.svc.FundPubKey
	approval, err := f.request(t, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(approval.ID)
	saved := f.svc.FundPubKey
	f.svc.FundPubKey = ""
	_, err = f.decide(t, 1, id, true, "")
	nodeCode(t, err, httpx.ErrMaintenance)
	f.svc.FundPubKey = saved

	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM receive_addresses`).Scan(&count); err != nil ||
		count != 0 {
		t.Fatal("没有验签能力却入库了", count, err)
	}
}

func TestAddressImportReusesPendingApprovalForSamePackageIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	in := AddressImportInput{Reason: "导入", Package: f.pkg(f.sign("MOCK", "MOCKSAME1", until))}

	first, err := f.request(t, 0, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.request(t, 0, in)
	if err != nil || again.ID != first.ID {
		t.Fatal("同一个包重复发起应当复用原审批单", first.ID, again.ID, err)
	}
	// 换个人发同一个包：明确冲突，不堆第二张。
	_, err = f.request(t, 1, in)
	nodeCode(t, err, httpx.ErrConflict)

	var approvals int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_approvals`).Scan(&approvals); err != nil ||
		approvals != 1 {
		t.Fatal("审批单数量", approvals, err)
	}
}

func TestAddressImportCannotBeSelfApprovedIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	approval, err := f.request(t, 0, AddressImportInput{Reason: "导入",
		Package: f.pkg(f.sign("MOCK", "MOCKSELF1", until))})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(approval.ID)
	// 自己批自己：双人审批的意义就在这里。
	_, err = f.decide(t, 0, id, true, "")
	nodeCode(t, err, httpx.ErrApproverIsAuthor)

	// 发起人被撤权之后，另一个人也不该能把它执行掉。
	if _, err = f.owner.Exec(f.ctx, `DELETE FROM admin_role_permissions
 WHERE role_id=$1 AND permission_code='finance.address_import'`, f.role); err != nil {
		t.Fatal(err)
	}
	_, err = f.decide(t, 1, id, true, "")
	nodeCode(t, err, httpx.ErrForbidden)

	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM receive_addresses`).Scan(&count); err != nil ||
		count != 0 {
		t.Fatal("不该执行却入库了", count, err)
	}
}

// 执行器必须同时认 action_code 与 target_type。只认其一的话，一张 target_type 被改过的
// 审批单就会被喂给错误的执行器——审批单的 payload 与执行逻辑是配套的，错配就是拿一种资料
// 去跑另一种动作。
func TestAddressImportExecutorMatchesTargetTypeIntegration(t *testing.T) {
	f := newImportFixture(t)
	until := f.clock.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	approval, err := f.request(t, 0, AddressImportInput{Reason: "导入",
		Package: f.pkg(f.sign("MOCK", "MOCKTYPE1", until))})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(approval.ID)
	// 把 target_type 改成别的动作用的那种，其余一切不变。
	if _, err = f.owner.Exec(f.ctx, `UPDATE admin_approvals SET target_type='app_version'
 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	_, err = f.decide(t, 1, id, true, "")
	nodeCode(t, err, httpx.ErrMaintenance)

	var count int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM receive_addresses`).Scan(&count); err != nil ||
		count != 0 {
		t.Fatal("错配的审批单被执行了", count, err)
	}
}
