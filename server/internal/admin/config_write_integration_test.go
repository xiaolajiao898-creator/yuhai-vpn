package admin

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 配置写这一面，值得写测试的全是「写进去不会报错、只会静默失效」的那几种：
//
//   - 白名单外的键（包括库里已经存在的）必须拒绝，否则等于给运维一个改了不生效的按钮；
//   - 充值上下限是一对，单独改一个能把区间改成空集；
//   - 邀请奖励的套餐编号必须用与发放侧同一套判断校验，否则奖励会静默不发；
//   - 落库的必须是规范化后的值，不是请求里的原始字节；
//   - 幂等：同键换请求体 40901，重放逐字节相同，并发只写一次。

type configFixture struct {
	*managedUserFixture
	writer *ConfigWriteService
}

func newConfigFixture(t *testing.T) *configFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"system_configs", "plans"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00031_admin_config_write.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='configs.write' AND risk_level=2`) != 1 {
		t.Fatal("configs.write 能力未定义或风险等级不对")
	}

	// 这就是本接口真实需要的权限清单：配置表读写、套餐只读（校验奖励编号用）、幂等表插入。
	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{f.schema, "system_configs"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT ON "+pgx.Identifier{f.schema, "plans"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{f.schema, "admin_config_mutations"}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'configs.write')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, actionConfigWrite)
	}
	// 与种子数据同样的一对充值上下限。
	f.write(t, `INSERT INTO system_configs(key,value) VALUES('topup.min_micro','1000000'),
 ('topup.max_micro','1000000000'),('order.cooldown_seconds','7200')`)
	return &configFixture{managedUserFixture: f, writer: &ConfigWriteService{Security: f.svc}}
}

func (f *configFixture) configProof(t *testing.T, key string, in ConfigUpdateInput) ApprovalProof {
	t.Helper()
	path := "/admin/v1/configs/" + key
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, actionConfigWrite, path, string(raw))
	p.RequestHash = RequestHash("PATCH", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func configInput(value string) ConfigUpdateInput {
	return ConfigUpdateInput{OperationID: uuid.NewString(), Reason: "调整参数",
		Value: json.RawMessage(value)}
}

// update 是「构造证明 → 调用」的快捷方式。
func (f *configFixture) update(t *testing.T, key string, in ConfigUpdateInput) (ManagedConfig, error) {
	t.Helper()
	return f.writer.Update(f.ctx, f.configProof(t, key, in), key, in)
}

func TestConfigWriteNormalizesAndBumpsVersionIntegration(t *testing.T) {
	f := newConfigFixture(t)

	// 带空白与前导加号的写法要么被拒、要么被规范化；这里用带空白的合法整数。
	out, err := f.update(t, "order.cooldown_seconds", configInput(" 3600 "))
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Value) != "3600" || out.Version != "2" || out.Created {
		t.Fatal("规范化或版本号不对", out)
	}
	// 落库的必须是规范化后的那份，不是请求里的原始字节。
	var stored string
	if err = f.owner.QueryRow(f.ctx, `SELECT value::text FROM system_configs
 WHERE key='order.cooldown_seconds'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "3600" {
		t.Fatal("库里存的不是规范化后的值", stored)
	}
	// updated_by 要记到具体的管理员，不是空。
	var updatedBy *int64
	if err = f.owner.QueryRow(f.ctx, `SELECT updated_by FROM system_configs
 WHERE key='order.cooldown_seconds'`).Scan(&updatedBy); err != nil {
		t.Fatal(err)
	}
	if updatedBy == nil || *updatedBy <= 0 {
		t.Fatal("没有记录修改人", updatedBy)
	}

	// 尚未配置过的键：写入第一条，created=true，version 从 1 开始。
	f.write(t, `INSERT INTO plans(code,name,plan_type,tier,duration_days,period_quota_bytes,
 device_limit,price_micro,status) VALUES('reward-time','奖励时长','TIME',1,30,NULL,2,100000,'ON_SALE')`)
	first, err := f.update(t, "invite.reward_default_plan_code", configInput(`"reward-time"`))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Version != "1" || string(first.Value) != `"reward-time"` {
		t.Fatal("首次写入", first)
	}
}

func TestConfigWriteRejectsKeysNobodyReadsIntegration(t *testing.T) {
	f := newConfigFixture(t)
	// 库里已经存在、但代码里没有读取方的键，同样要拒绝。
	f.write(t, `INSERT INTO system_configs(key,value) VALUES('legacy.unused_flag','true')`)
	for _, key := range []string{"legacy.unused_flag", "topup.min", "TOPUP.MIN_MICRO", ""} {
		_, err := f.update(t, key, configInput("1"))
		nodeCode(t, err, httpx.ErrBadRequest)
	}
	// 拒绝要彻底：那条既有记录不能被动过。
	var v string
	if err := f.owner.QueryRow(f.ctx, `SELECT value::text FROM system_configs
 WHERE key='legacy.unused_flag'`).Scan(&v); err != nil || v != "true" {
		t.Fatal("被拒的请求改动了数据", v, err)
	}
}

func TestConfigWriteRejectsBadValuesIntegration(t *testing.T) {
	f := newConfigFixture(t)
	for name, tc := range map[string]struct{ key, value string }{
		"冷却秒数带小数":  {"order.cooldown_seconds", "3600.5"},
		"冷却秒数低于下限": {"order.cooldown_seconds", "59"},
		"冷却秒数高于上限": {"order.cooldown_seconds", "604801"},
		"冷却秒数是字符串": {"order.cooldown_seconds", `"3600"`},
		"充值下限为零":   {"topup.min_micro", "0"},
		"充值下限是负数":  {"topup.min_micro", "-1"},
		"套餐编号是数字":  {"invite.reward_default_plan_code", "123"},
		"套餐编号为空串":  {"invite.reward_default_plan_code", `""`},
	} {
		_, err := f.update(t, tc.key, configInput(tc.value))
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 上面每一条都不该落库：三个键的现值必须原样。
	var n int
	if err := f.owner.QueryRow(f.ctx, `SELECT count(*) FROM system_configs
 WHERE (key='order.cooldown_seconds' AND value::text='7200')
    OR (key='topup.min_micro' AND value::text='1000000')`).Scan(&n); err != nil || n != 2 {
		t.Fatal("被拒的请求改动了数据", n, err)
	}
}

func TestConfigWriteKeepsTopupRangeConsistentIntegration(t *testing.T) {
	f := newConfigFixture(t)
	// 下限调到高于现有上限：必须被挡住，否则下单区间成为空集。
	_, err := f.update(t, "topup.min_micro", configInput("2000000000"))
	nodeCode(t, err, httpx.ErrBadRequest)
	// 上限调到低于现有下限：同样挡住。
	_, err = f.update(t, "topup.max_micro", configInput("999999"))
	nodeCode(t, err, httpx.ErrBadRequest)
	// 等于是允许的（区间退化成一个点，但不是空集）。
	if _, err = f.update(t, "topup.min_micro", configInput("1000000000")); err != nil {
		t.Fatal("下限等于上限应当放行", err)
	}
	// 另一端从未配置过时放行：读取方取不到会退回内置默认值，此时没有「一对」可言。
	f.write(t, `DELETE FROM system_configs WHERE key='topup.max_micro'`)
	if _, err = f.update(t, "topup.min_micro", configInput("5000000000")); err != nil {
		t.Fatal("另一端缺失时应当放行", err)
	}
}

func TestConfigWriteValidatesRewardPlanLikeTheIssuerIntegration(t *testing.T) {
	f := newConfigFixture(t)
	// 四种「写进去奖励会静默不发」的编号，逐个挡住。
	f.write(t, `INSERT INTO plans(code,name,plan_type,tier,duration_days,period_quota_bytes,
 device_limit,price_micro,status) VALUES
 ('offline-time','已下架','TIME',1,30,NULL,2,100000,'OFF_SALE'),
 ('traffic-pack','流量包','TRAFFIC',1,NULL,NULL,2,100000,'ON_SALE'),
 ('draft-time','还是草稿','TIME',1,30,NULL,2,100000,'DRAFT')`)
	f.write(t, `UPDATE plans SET pack_bytes=1073741824,validity_days=30 WHERE code='traffic-pack'`)
	for name, code := range map[string]string{
		"不存在的编号": "never-existed",
		"已下架":    "offline-time",
		"类型不符":   "traffic-pack", // 时长类奖励却指向流量包
		"还是草稿":   "draft-time",
	} {
		_, err := f.update(t, "invite.reward_default_plan_code", configInput(`"`+code+`"`))
		if err == nil {
			t.Fatalf("%s 应当被拒绝", name)
		}
		nodeCode(t, err, httpx.ErrBadRequest)
	}
	// 流量类奖励指向流量包则通过——证明拒绝的是类型不符，不是一概拒绝。
	if _, err := f.update(t, "invite.reward_traffic_plan_code", configInput(`"traffic-pack"`)); err != nil {
		t.Fatal("类型匹配的编号应当放行", err)
	}
}

func TestConfigWriteIsIdempotentIntegration(t *testing.T) {
	f := newConfigFixture(t)
	in := configInput("3600")
	first, err := f.update(t, "order.cooldown_seconds", in)
	if err != nil {
		t.Fatal(err)
	}
	// 同 operation_id、同请求体重放：返回首次结果，版本号不再增长。
	replay, err := f.update(t, "order.cooldown_seconds", in)
	firstRaw, _ := json.Marshal(first)
	replayRaw, _ := json.Marshal(replay)
	if err != nil || string(firstRaw) != string(replayRaw) {
		t.Fatal("重放结果与首次不逐字节相同", string(replayRaw), string(firstRaw), err)
	}
	var version int
	if err = f.owner.QueryRow(f.ctx, `SELECT version FROM system_configs
 WHERE key='order.cooldown_seconds'`).Scan(&version); err != nil || version != 2 {
		t.Fatal("重放又写了一次", version, err)
	}
	// 同 operation_id 换请求体：40901。
	changed := in
	changed.Value = json.RawMessage("1800")
	_, err = f.update(t, "order.cooldown_seconds", changed)
	nodeCode(t, err, httpx.ErrConflict)

	// 6 路并发同键：只成功一次，库里只留一条幂等记录。
	concurrent := configInput("1200")
	proofs := make([]ApprovalProof, 6)
	for i := range proofs {
		proofs[i] = f.configProof(t, "order.cooldown_seconds", concurrent)
	}
	var wg sync.WaitGroup
	results := make([]error, len(proofs))
	for i := range proofs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = f.writer.Update(f.ctx, proofs[i], "order.cooldown_seconds", concurrent)
		}(i)
	}
	wg.Wait()
	var ok int
	for _, e := range results {
		if e == nil {
			ok++
		}
	}
	if ok == 0 {
		t.Fatal("并发全部失败", results)
	}
	var rows int
	if err = f.owner.QueryRow(f.ctx, `SELECT count(*) FROM admin_config_mutations
 WHERE operation_id=$1`, concurrent.OperationID).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("并发写出了多条幂等记录", rows, err)
	}
	if err = f.owner.QueryRow(f.ctx, `SELECT version FROM system_configs
 WHERE key='order.cooldown_seconds'`).Scan(&version); err != nil || version != 3 {
		t.Fatal("并发把版本号推高了不止一次", version, err)
	}
}

func TestConfigWriteRequiresStepUpAndPermissionIntegration(t *testing.T) {
	f := newConfigFixture(t)
	in := configInput("3600")

	// 没有二次验证令牌。
	p := f.configProof(t, "order.cooldown_seconds", in)
	p.Token = ""
	_, err := f.writer.Update(f.ctx, p, "order.cooldown_seconds", in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	// 有令牌但权限被撤销。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='configs.write'`, f.role)
	_, err = f.update(t, "order.cooldown_seconds", configInput("3600"))
	nodeCode(t, err, httpx.ErrForbidden)

	// 两次失败都不该改动配置。
	var v string
	if err = f.owner.QueryRow(f.ctx, `SELECT value::text FROM system_configs
 WHERE key='order.cooldown_seconds'`).Scan(&v); err != nil || v != "7200" {
		t.Fatal("失败的请求改动了配置", v, err)
	}
}
