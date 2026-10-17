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

// 改奖励规则的测试重点，全在「改错了不会报错、只会把人工队列灌满」这件事上：
//
//   - 形状不合格的规则必须在写入时挡住，用的是与发放侧同一份判断；
//   - trigger_event / beneficiary / reward_type 不可改（前两个是去重键，改了会重复发奖）；
//   - version 只在实质字段变化时 +1，改名字不动它；
//   - 已经生成的奖励一条都不受影响。

type inviteRuleFixture struct {
	*managedUserFixture
	writer *InviteRuleWriteService
	ruleID int64
}

func newInviteRuleFixture(t *testing.T) *inviteRuleFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"invite_reward_rules", "invite_rewards"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	for _, file := range []string{"00028_admin_invite_review.sql", "00032_admin_invite_rule_write.sql"} {
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
 WHERE code='invite.rule_write' AND risk_level=2`) != 1 {
		t.Fatal("invite.rule_write 能力未定义或风险等级不对")
	}

	f.write(t, "GRANT SELECT,UPDATE ON "+pgx.Identifier{f.schema, "invite_reward_rules"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT ON "+pgx.Identifier{f.schema, "invite_rewards"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{f.schema, "admin_invite_mutations"}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'invite.rule_write')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, actionInviteRuleWrite)
	}

	out := &inviteRuleFixture{managedUserFixture: f,
		writer: &InviteRuleWriteService{Security: f.svc}}
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO invite_reward_rules(name,trigger_event,
 beneficiary,reward_type,reward_value,freeze_hours,status,version)
 VALUES('首单奖励','INVITEE_FIRST_PAID','INVITER','DAYS',30,72,'ENABLED',1) RETURNING id`).
		Scan(&out.ruleID); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *inviteRuleFixture) ruleProof(t *testing.T, in InviteRuleUpdateInput) ApprovalProof {
	t.Helper()
	path := "/admin/v1/invite-rules/" + strconv.FormatInt(f.ruleID, 10)
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, actionInviteRuleWrite, path, string(raw))
	p.RequestHash = RequestHash("PATCH", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func (f *inviteRuleFixture) patch(t *testing.T, in InviteRuleUpdateInput) (ManagedInviteRule, error) {
	t.Helper()
	if in.OperationID == "" {
		in.OperationID = uuid.NewString()
	}
	if in.Reason == "" {
		in.Reason = "调整规则"
	}
	return f.writer.Update(f.ctx, f.ruleProof(t, in), f.ruleID, in)
}

func ptr[T any](v T) *T { return &v }

func TestInviteRuleUpdateBumpsVersionOnlyForSubstantiveChangesIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)

	// 改名字：不动 version——奖励快照里记着它，每改个名字就推高会让它失去对账价值。
	renamed, err := f.patch(t, InviteRuleUpdateInput{Name: ptr("首单奖励（新）")})
	if err != nil || renamed.Version != "1" || renamed.Name != "首单奖励（新）" {
		t.Fatal("改名字不该推高版本号", renamed, err)
	}
	// 上下架同理不动 version。
	off, err := f.patch(t, InviteRuleUpdateInput{Status: ptr("DISABLED")})
	if err != nil || off.Version != "1" || off.Status != "DISABLED" {
		t.Fatal("上下架不该推高版本号", off, err)
	}
	// 改奖励数值：实质变化，version +1。
	bumped, err := f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(15))})
	if err != nil || bumped.Version != "2" || bumped.RewardValue != "15" {
		t.Fatal("实质修改应当推高版本号", bumped, err)
	}
	// 改冻结小时数也算实质变化。
	frozen, err := f.patch(t, InviteRuleUpdateInput{Freeze: ptr(24)})
	if err != nil || frozen.Version != "3" || frozen.FreezeHours != 24 {
		t.Fatal("冻结小时数", frozen, err)
	}
	// 没提的字段原样保留：这是 PATCH 不是整条替换。
	if frozen.Name != "首单奖励（新）" || frozen.RewardValue != "15" || frozen.Status != "DISABLED" {
		t.Fatal("没提的字段被清掉了", frozen)
	}
	// 显式 null 与「没提」要能区分：把最低订单金额设上再清掉。
	withMin, err := f.patch(t, InviteRuleUpdateInput{Minimum: ptr(ptr(int64(5_000_000)))})
	if err != nil || withMin.MinOrder == nil || *withMin.MinOrder != "5000000" {
		t.Fatal("设置最低订单金额", withMin, err)
	}
	cleared, err := f.patch(t, InviteRuleUpdateInput{Minimum: ptr((*int64)(nil))})
	if err != nil || cleared.MinOrder != nil {
		t.Fatal("显式 null 应当清空", cleared, err)
	}
}

func TestInviteRuleUpdateRejectsShapesTheIssuerWouldFlagIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)
	for name, in := range map[string]InviteRuleUpdateInput{
		// 按比例只能配在 BALANCE 上——配在 DAYS 上，发放侧每笔都打 INVALID_RULE。
		"比例配在非余额类型上": {Percent: ptr(ptr(500))},
		// DAYS 超过上限。
		"天数超过 36500": {Value: ptr(int64(36501))},
		// 固定值与比例都为 0：每笔都会被判 ZERO_REWARD 直接作废。
		"数值为零": {Value: ptr(int64(0))},
		// 每日上限显式写 0：发放侧会把每笔都打成 DAILY_CAP_EXCEEDED。
		"每日上限为零":  {Cap: ptr(ptr(0))},
		"每日上限为负":  {Cap: ptr(ptr(-1))},
		"冻结小时数为负": {Freeze: ptr(-1)},
		"状态取值非法":  {Status: ptr("PAUSED")},
		"名称为空":    {Name: ptr("")},
		"什么都没改":   {},
	} {
		_, err := f.patch(t, in)
		var coded *httpx.Error
		if !errors.As(err, &coded) || coded.Code != httpx.ErrBadRequest {
			t.Fatalf("%s 应当被拒绝，得到 %v", name, err)
		}
	}
	// 一条都不该落库。
	var value int64
	var version int
	if err := f.owner.QueryRow(f.ctx, `SELECT reward_value,version FROM invite_reward_rules
 WHERE id=$1`, f.ruleID).Scan(&value, &version); err != nil || value != 30 || version != 1 {
		t.Fatal("被拒的请求改动了规则", value, version, err)
	}
}

func TestInviteRuleUpdateRequiresDailyCapForEveryPaidOrderIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)
	// INVITEE_PAID 每笔订单都触发；没有每日上限时发放侧会把每一笔都打进人工复核。
	// trigger_event 不可改，所以直接造一条 INVITEE_PAID 的规则来验证这条。
	var paidRule int64
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO invite_reward_rules(name,trigger_event,
 beneficiary,reward_type,reward_value,freeze_hours,status,version)
 VALUES('每单奖励','INVITEE_PAID','INVITER','BALANCE',1000,72,'ENABLED',1) RETURNING id`).
		Scan(&paidRule); err != nil {
		t.Fatal(err)
	}
	prev := f.ruleID
	f.ruleID = paidRule
	defer func() { f.ruleID = prev }()

	// 只改数值、仍然没有每日上限：必须被挡住。
	_, err := f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(2000))})
	nodeCode(t, err, httpx.ErrBadRequest)
	// 补上每日上限后同样的修改就能过。
	out, err := f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(2000)), Cap: ptr(ptr(5))})
	if err != nil || out.DailyCap == nil || *out.DailyCap != 5 {
		t.Fatal("补上每日上限后应当放行", out, err)
	}
}

func TestInviteRuleUpdateKeepsWindowAndConcurrencyGuardsIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)
	now := f.clock.Now().UTC()
	// 窗口是 [starts_at, ends_at)，右端开区间：相等即空窗口。
	_, err := f.patch(t, InviteRuleUpdateInput{StartsAt: ptr(&now), EndsAt: ptr(&now)})
	nodeCode(t, err, httpx.ErrBadRequest)
	later := now.Add(time.Hour)
	if _, err = f.patch(t, InviteRuleUpdateInput{StartsAt: ptr(&now), EndsAt: ptr(&later)}); err != nil {
		t.Fatal("合法窗口应当放行", err)
	}
	// 乐观并发：带上过期的 expected_version 必须整笔拒绝而不是覆盖。
	_, err = f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(20)), ExpectedNo: ptr("1")})
	nodeCode(t, err, httpx.ErrConflict)
	// 带上当前版本就能过。
	var current string
	if err = f.owner.QueryRow(f.ctx, `SELECT version::text FROM invite_reward_rules WHERE id=$1`,
		f.ruleID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if _, err = f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(20)), ExpectedNo: &current}); err != nil {
		t.Fatal("版本相符应当放行", err)
	}
}

func TestInviteRuleUpdateLeavesIssuedRewardsAloneIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)
	// 一条已生成的奖励：它按自己的 rule_snapshot 结算，改规则不该动它分毫。
	f.write(t, `INSERT INTO invite_rewards(rule_id,rule_snapshot,inviter_user_id,invitee_user_id,
 beneficiary_user_id,trigger_ref,reward_type,reward_value,status,release_at,created_at)
 VALUES($1,'{"snapshot_version":1}',$2,$2,$2,'first_paid','DAYS',30,'PENDING',$3,$3)`,
		f.ruleID, f.userID, f.clock.Now())

	if _, err := f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(1)), Status: ptr("DISABLED")}); err != nil {
		t.Fatal(err)
	}
	var value int64
	var status string
	var snapshot string
	if err := f.owner.QueryRow(f.ctx, `SELECT reward_value,status,rule_snapshot::text
 FROM invite_rewards WHERE rule_id=$1`, f.ruleID).Scan(&value, &status, &snapshot); err != nil {
		t.Fatal(err)
	}
	if value != 30 || status != "PENDING" || !strings.Contains(snapshot, "snapshot_version") {
		t.Fatal("改规则动了已经生成的奖励", value, status, snapshot)
	}
}

func TestInviteRuleUpdateRequiresStepUpAndPermissionIntegration(t *testing.T) {
	f := newInviteRuleFixture(t)
	in := InviteRuleUpdateInput{OperationID: uuid.NewString(), Reason: "调整规则", Value: ptr(int64(15))}

	p := f.ruleProof(t, in)
	p.Token = ""
	_, err := f.writer.Update(f.ctx, p, f.ruleID, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='invite.rule_write'`, f.role)
	_, err = f.patch(t, InviteRuleUpdateInput{Value: ptr(int64(15))})
	nodeCode(t, err, httpx.ErrForbidden)

	var value int64
	if err = f.owner.QueryRow(f.ctx, `SELECT reward_value FROM invite_reward_rules WHERE id=$1`,
		f.ruleID).Scan(&value); err != nil || value != 30 {
		t.Fatal("失败的请求改动了规则", value, err)
	}
}
