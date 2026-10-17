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
)

// 复核这条路的测试重点，是它与别的后台写操作不一样的那几件事：
//
//   - **放行会当场记账**：必须验证钱真的到了账，而不是只把状态改成 RELEASED。
//   - **复核不重跑风控，但完整性检查一条不省**：来源订单改了、关系没了、
//     受益人被封了、账已经记过了——每一条都必须挡住，且整笔回滚。
//   - **只处置 REVIEW**：PENDING 归 worker，已成定局的不许翻案。
//   - **与 worker 共用同一把幂等 key**：所以先记账后复核，或反过来，都不会发两次。

type inviteFixture struct {
	*managedUserFixture
	read    *InviteReadService
	review  *InviteReviewService
	inviter int64
	invitee int64
	orderID int64
	paidAt  time.Time
	base    int64
	ruleID  int64
	seq     int
}

func newInviteFixture(t *testing.T) *inviteFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"invite_relations", "invite_rewards", "orders", "traffic_packs"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00028_admin_invite_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='invite.reward_review' AND risk_level=2`) != 1 {
		t.Fatal("复核能力未定义或风险等级不对")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code='invite.read' AND risk_level=0`) != 1 {
		t.Fatal("invite.read 能力未定义")
	}

	// 这批授权就是本接口真实需要的权限清单。
	//
	// 注意 orders 与 invite_relations 上那两条 UPDATE(created_at)：**代码永远不会写这两列**。
	// 授它们纯粹是因为 PostgreSQL 规定 SELECT ... FOR SHARE 至少需要该表某一列的 UPDATE 权限，
	// 而复核必须锁住来源订单与邀请关系——否则在核对与提交之间，订单被退款也照发不误。
	// 这是一处真实的权限代价，写在这里免得部署时误以为 admin-api 对这两张表只需只读。
	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{"invite_rewards"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,UPDATE(created_at) ON "+pgx.Identifier{"orders"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,UPDATE(created_at) ON "+pgx.Identifier{"invite_relations"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT UPDATE(status) ON "+pgx.Identifier{"users"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{"user_balances"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{"balance_ledger"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{"admin_invite_mutations"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role)

	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'invite.read'),($1,'invite.reward_review')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, inviteRead, actionRewardReview)
	}

	out := &inviteFixture{managedUserFixture: f, read: &InviteReadService{Auth: f.svc.Auth},
		review: &InviteReviewService{Security: f.svc},
		// 受益人（邀请人）用一个干净的账户：夹具自带的 userID 余额已是 int64 上限，
		// 再加钱会溢出——那是另一个用例要用的条件。
		inviter: 9007199254740995, invitee: 9007199254740996,
		// 微秒：PostgreSQL 的 timestamptz 精度到微秒，而快照里的时间是 JSON 里的纳秒。
		// 不截断的话，「订单没变」这条核验会因为精度差异而永远失败。
		paidAt: f.clock.Now().UTC().Truncate(time.Microsecond), base: 10_000_000, ruleID: 77,
	}
	f.user(t, out.inviter, "7000000001", "ACTIVE")
	f.user(t, out.invitee, "7000000002", "ACTIVE")
	f.write(t, `INSERT INTO invite_relations(invitee_user_id,inviter_user_id,invite_code,bind_method,
 risk_flags,created_at) VALUES($1,$2,'INV0001','CLIPBOARD','["CLOSE_REGISTRATION_IP"]',$3)`,
		out.invitee, out.inviter, out.paidAt.Add(-time.Hour))
	// TOPUP + USDT_TRC20：约束要求 receive_address_id 非空，测试 schema 里没有外键，
	// 给一个编号即可——复核只核对金额与时间，不读收款地址。
	if err = f.owner.QueryRow(f.ctx, `INSERT INTO orders(order_no,user_id,order_type,base_amount_micro,
 pay_amount_micro,pay_method,receive_address_id,status,fulfillment_result,client_idem_key,expires_at,
 paid_at,completed_at,created_at,updated_at)
 VALUES('INVITE-REVIEW-1',$1,'TOPUP',$2,$2,'USDT_TRC20',1,'COMPLETED','CREDITED_TO_BALANCE',
 'invite-review-key',$3,$3,$3,$3,$3) RETURNING id`, out.invitee, out.base, out.paidAt).
		Scan(&out.orderID); err != nil {
		t.Fatal(err)
	}
	return out
}

// snapshot 生成一份与奖励行自洽的规则快照——复核的完整性检查就是拿它跟订单对账的。
func (f *inviteFixture) snapshot(kind string, value int64) string {
	stamp := f.paidAt.Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"snapshot_version":1,"rule":{"id":%d,"name":"首单奖励",
 "trigger_event":"INVITEE_FIRST_PAID","beneficiary":"INVITER","reward_type":%q,"reward_value":%d,
 "reward_percent_bp":null,"min_order_amount_micro":null,"freeze_hours":72,
 "daily_cap_per_inviter":null,"version":1,"starts_at":null,"ends_at":null},
 "order_id":%d,"order_base_micro":%d,"order_pay_micro":%d,"paid_at":%q,"completed_at":%q,
 "calculated_value":%d}`, f.ruleID, kind, value, f.orderID, f.base, f.base, stamp, stamp, value)
}

func (f *inviteFixture) reward(t *testing.T, kind string, value int64, status string) int64 {
	t.Helper()
	// 同一 (rule_id, 受益人, 被邀请人, trigger_ref) 只能有一条：一个用例里造多笔奖励时
	// 必须换 trigger_ref，否则撞的是业务上「同一次触发不得重复发奖」那条唯一约束。
	f.seq++
	var id int64
	if err := f.owner.QueryRow(f.ctx, `INSERT INTO invite_rewards(rule_id,rule_snapshot,inviter_user_id,
 invitee_user_id,beneficiary_user_id,trigger_ref,reward_type,reward_value,status,risk_flags,
 release_at,created_at) VALUES($1,$2,$3,$4,$3,$9,$5,$6,$7,'["ACCOUNT_RISK"]',$8,$8)
 RETURNING id`, f.ruleID, f.snapshot(kind, value), f.inviter, f.invitee, kind, value, status,
		f.paidAt, fmt.Sprintf("first_paid_%d", f.seq)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *inviteFixture) reviewProof(t *testing.T, id int64, in RewardReviewInput) ApprovalProof {
	t.Helper()
	path := fmt.Sprintf("/admin/v1/invite-rewards/%d/review", id)
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, actionRewardReview, path, string(raw))
	p.RequestHash = RequestHash("POST", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`,
		tokenHash(p.Token), p.RequestHash)
	return p
}

func (f *inviteFixture) rewardStatus(t *testing.T, id int64) (string, []string) {
	t.Helper()
	var status string
	var flags []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT status,risk_flags FROM invite_rewards WHERE id=$1`, id).
		Scan(&status, &flags); err != nil {
		t.Fatal(err)
	}
	var out []string
	if err := json.Unmarshal(flags, &out); err != nil {
		t.Fatal(err)
	}
	return status, out
}

func TestInviteRewardApproveCreditsAndIsIdempotentIntegration(t *testing.T) {
	f := newInviteFixture(t)
	const value = int64(5_000_000)
	id := f.reward(t, "BALANCE", value, "REVIEW")
	in := RewardReviewInput{OperationID: uuid.NewString(), Decision: "APPROVE", Note: "人工核对邀请链路属实"}
	p := f.reviewProof(t, id, in)

	out, err := f.review.Review(f.ctx, p, id, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "RELEASED" || out.Beneficiary != f.inviter || out.Value != value {
		t.Fatalf("处置结果不符：%+v", out)
	}
	// 处置前的风险标记必须原样带回——运维要知道自己放行的是什么。
	if len(out.RiskFlags) != 1 || out.RiskFlags[0] != "ACCOUNT_RISK" {
		t.Fatalf("未带回处置前的风险标记：%v", out.RiskFlags)
	}

	status, flags := f.rewardStatus(t, id)
	if status != "RELEASED" || flags[len(flags)-1] != "ADMIN_APPROVED" {
		t.Fatalf("奖励状态或标记不符：%s %v", status, flags)
	}
	var releasedAt *time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT released_at FROM invite_rewards WHERE id=$1`, id).
		Scan(&releasedAt); err != nil {
		t.Fatal(err)
	}
	if releasedAt == nil {
		t.Fatal("放行没有记下发放时间")
	}

	// 钱必须真的到账，而不是只改了状态。
	key := "invite_reward:" + strconv.FormatInt(id, 10)
	var amountMicro, balance int64
	if err = f.pool.QueryRow(f.ctx, `SELECT l.amount_micro,b.available_micro FROM balance_ledger l
 JOIN user_balances b ON b.user_id=l.user_id WHERE l.idempotency_key=$1`, key).
		Scan(&amountMicro, &balance); err != nil {
		t.Fatalf("放行没有记账：%v", err)
	}
	if amountMicro != value || balance != value {
		t.Fatalf("入账金额不符：流水 %d 余额 %d", amountMicro, balance)
	}

	// 同一 operation_id 重复提交：返回首次结果，且绝不重复发放。
	again, err := f.review.Review(f.ctx, p, id, in)
	if err != nil {
		t.Fatalf("重复提交应当幂等：%v", err)
	}
	if again.Status != out.Status || again.Value != out.Value {
		t.Fatalf("重放返回了不同结果：%+v ≠ %+v", again, out)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM balance_ledger
 WHERE idempotency_key LIKE 'invite_reward:%'`); n != 1 {
		t.Fatalf("重放发了 %d 次", n)
	}
}

func TestInviteRewardRejectTouchesNoLedgerIntegration(t *testing.T) {
	f := newInviteFixture(t)
	id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
	in := RewardReviewInput{OperationID: uuid.NewString(), Decision: "REJECT", Note: "同一 IP 段批量注册"}

	out, err := f.review.Review(f.ctx, f.reviewProof(t, id, in), id, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "CANCELLED" {
		t.Fatalf("驳回结果不符：%+v", out)
	}
	var reason *string
	var releasedAt *time.Time
	if err = f.pool.QueryRow(f.ctx, `SELECT cancel_reason,released_at FROM invite_rewards WHERE id=$1`, id).
		Scan(&reason, &releasedAt); err != nil {
		t.Fatal(err)
	}
	if reason == nil || *reason != in.Note || releasedAt != nil {
		t.Fatalf("驳回留痕不符：%v %v", reason, releasedAt)
	}
	if _, flags := f.rewardStatus(t, id); flags[len(flags)-1] != "ADMIN_REJECTED" {
		t.Fatalf("驳回没有留下标记：%v", flags)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM balance_ledger
 WHERE idempotency_key LIKE 'invite_reward:%'`); n != 0 {
		t.Fatalf("驳回却记了 %d 笔账", n)
	}
	// 已成定局的不许翻案。
	redo := RewardReviewInput{OperationID: uuid.NewString(), Decision: "APPROVE", Note: "改主意了"}
	_, err = f.review.Review(f.ctx, f.reviewProof(t, id, redo), id, redo)
	nodeCode(t, err, httpx.ErrConflict)
}

func TestInviteRewardApproveRefusesBrokenIntegrityIntegration(t *testing.T) {
	// want 是**必须出现在错误消息里的原因码**。只断言 40901 是不够的：
	// 七种现场都会返回 40901，不核对原因码就分辨不出「挡住了」和「因为别的原因挡住了」。
	cases := []struct {
		name, want string
		// break 制造一种「复核放行会出错」的现场，返回被处置奖励的编号。
		breakIt func(t *testing.T, f *inviteFixture) int64
	}{
		{"不在复核状态", "STATUS_NOT_REVIEW:PENDING", func(t *testing.T, f *inviteFixture) int64 {
			return f.reward(t, "BALANCE", 5_000_000, "PENDING") // PENDING 归 worker
		}},
		{"来源订单金额被改", "SOURCE_ORDER_CHANGED", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `UPDATE orders SET base_amount_micro=base_amount_micro+1,
 pay_amount_micro=pay_amount_micro+1 WHERE id=$1`, f.orderID)
			return id
		}},
		{"邀请关系已不在", "RELATION_MISSING", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `DELETE FROM invite_relations WHERE invitee_user_id=$1`, f.invitee)
			return id
		}},
		{"邀请人已换", "RELATION_CHANGED", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `UPDATE invite_relations SET inviter_user_id=$2 WHERE invitee_user_id=$1`,
				f.invitee, f.userID)
			return id
		}},
		{"受益人已被封禁", "BENEFICIARY_BANNED", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `UPDATE users SET status='BANNED' WHERE id=$1`, f.inviter)
			return id
		}},
		{"这笔账已经记过", "PREEXISTING_LEDGER", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `INSERT INTO balance_ledger(user_id,entry_type,amount_micro,balance_after_micro,
 source_type,source_id,idempotency_key,operator_type,remark,created_at)
 VALUES($1,'INVITE_REWARD',1,1,'INVITE_REWARD',$2,$3,'SYSTEM','worker 已发',$4)`,
				f.inviter, id, "invite_reward:"+strconv.FormatInt(id, 10), f.clock.Now())
			return id
		}},
		{"快照与奖励行对不上", "INVALID_SNAPSHOT", func(t *testing.T, f *inviteFixture) int64 {
			id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
			f.write(t, `UPDATE invite_rewards SET reward_value=reward_value*2 WHERE id=$1`, id)
			return id
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInviteFixture(t)
			id := tc.breakIt(t, f)
			in := RewardReviewInput{OperationID: uuid.NewString(), Decision: "APPROVE", Note: "放行试试"}
			_, err := f.review.Review(f.ctx, f.reviewProof(t, id, in), id, in)
			nodeCode(t, err, httpx.ErrConflict)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("拒绝原因不符：期望含 %s，实际 %v", tc.want, err)
			}

			// 整笔回滚：状态不变，也没留下幂等记录——否则换个理由重试都做不到。
			if status, _ := f.rewardStatus(t, id); status != "PENDING" && status != "REVIEW" {
				t.Fatalf("被拒的复核改了状态：%s", status)
			}
			if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_invite_mutations`); n != 0 {
				t.Fatalf("被拒的复核写了 %d 条幂等记录", n)
			}
			// 「这笔账已经记过」那一例本来就有一条流水，放行不得再加一条。
			if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM balance_ledger
 WHERE idempotency_key LIKE 'invite_reward:%'`); n > 1 {
				t.Fatalf("被拒的复核记了 %d 笔账", n)
			}
		})
	}

	// 不存在的奖励是 404，不是 409——运维要采取的动作不同。
	f := newInviteFixture(t)
	in := RewardReviewInput{OperationID: uuid.NewString(), Decision: "APPROVE", Note: "查无此奖励"}
	_, err := f.review.Review(f.ctx, f.reviewProof(t, 999999, in), 999999, in)
	nodeCode(t, err, httpx.ErrNotFound)
}

func TestInviteReviewRequiresStepUpAndOwnPermissionIntegration(t *testing.T) {
	f := newInviteFixture(t)
	id := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
	in := RewardReviewInput{OperationID: uuid.NewString(), Decision: "APPROVE", Note: "核对无误"}

	bare := f.reviewProof(t, id, in)
	bare.Token = ""
	_, err := f.review.Review(f.ctx, bare, id, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	wrong := f.reviewProof(t, id, in)
	wrong.RequestHash = RequestHash("POST", "/admin/v1/invite-rewards/1/review", []byte("{}"))
	_, err = f.review.Review(f.ctx, wrong, id, in)
	nodeCode(t, err, httpx.ErrStepUpInvalid)

	bad := RewardReviewInput{OperationID: uuid.NewString(), Decision: "MAYBE", Note: "含糊其辞"}
	_, err = f.review.Review(f.ctx, f.reviewProof(t, id, bad), id, bad)
	nodeCode(t, err, httpx.ErrBadRequest)

	// 复核权限与查看权限是两回事：撤掉复核，列表照样能看。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code=$2`,
		f.role, actionRewardReview)
	_, err = f.review.Review(f.ctx, f.reviewProof(t, id, in), id, in)
	nodeCode(t, err, httpx.ErrForbidden)
	if _, err = f.read.Rewards(f.ctx, f.people[0], InviteFilter{}); err != nil {
		t.Fatalf("撤掉复核权限不该影响查看：%v", err)
	}
	if status, _ := f.rewardStatus(t, id); status != "REVIEW" {
		t.Fatalf("被拒的请求改了状态：%s", status)
	}
}

func TestInviteListsRenderUnitsAndFilterIntegration(t *testing.T) {
	f := newInviteFixture(t)
	balanceID := f.reward(t, "BALANCE", 5_000_000, "REVIEW")
	f.reward(t, "TRAFFIC", 10_737_418_240, "PENDING")
	f.reward(t, "DAYS", 30, "RELEASED")
	p := f.people[0]

	page, err := f.read.Rewards(f.ctx, p, InviteFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != "3" || len(page.Items) != 3 {
		t.Fatalf("奖励列表不符：%s %d", page.Total, len(page.Items))
	}
	units := map[string]string{}
	for _, v := range page.Items {
		units[v.RewardType] = v.ValueUnit
		// 金额只在 BALANCE 上出现——否则运维会把 107 亿字节当成 107 亿 USDT。
		if (v.RewardType == "BALANCE") != (v.Amount != nil) {
			t.Fatalf("amount 出现在了错误的类型上：%+v", v)
		}
		if v.RewardType == "BALANCE" && (*v.Amount != "5.000000" || v.RewardValue != "5000000") {
			t.Fatalf("金额渲染不符：%+v", v)
		}
		if v.RewardType == "TRAFFIC" && v.RewardValue != "10737418240" {
			t.Fatalf("流量值不该被改写：%+v", v)
		}
	}
	if units["BALANCE"] != "USDT_MICRO" || units["TRAFFIC"] != "BYTES" || units["DAYS"] != "DAYS" {
		t.Fatalf("单位标注不符：%v", units)
	}

	only, err := f.read.Rewards(f.ctx, p, InviteFilter{Status: "REVIEW"})
	if err != nil || len(only.Items) != 1 || only.Items[0].ID != strconv.FormatInt(balanceID, 10) {
		t.Fatalf("状态筛选不符：%v %+v", err, only.Items)
	}
	if _, err = f.read.Rewards(f.ctx, p, InviteFilter{Status: "REVIEWING"}); err == nil {
		t.Fatal("非法状态被接受了")
	}
	none, err := f.read.Rewards(f.ctx, p, InviteFilter{BeneficiaryID: strconv.FormatInt(f.invitee, 10)})
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("受益人筛选不符：%v %+v", err, none.Items)
	}

	rel, err := f.read.Relations(f.ctx, p, InviteFilter{})
	if err != nil || rel.Total != "1" || len(rel.Items) != 1 {
		t.Fatalf("邀请关系列表不符：%v %+v", err, rel)
	}
	if rel.Items[0].InviterUserID != strconv.FormatInt(f.inviter, 10) ||
		rel.Items[0].BindMethod != "CLIPBOARD" {
		t.Fatalf("邀请关系内容不符：%+v", rel.Items[0])
	}
	byInviter, err := f.read.Relations(f.ctx, p, InviteFilter{InviterUserID: strconv.FormatInt(f.invitee, 10)})
	if err != nil || len(byInviter.Items) != 0 {
		t.Fatalf("邀请人筛选不符：%v %+v", err, byInviter.Items)
	}
	if _, err = f.read.Relations(f.ctx, p, InviteFilter{InviterUserID: "0"}); err == nil {
		t.Fatal("非法用户编号被接受了")
	}

	// 撤掉 invite.read 之后一条都不许读。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='invite.read'`, f.role)
	_, err = f.read.Rewards(f.ctx, p, InviteFilter{})
	nodeCode(t, err, httpx.ErrForbidden)
	_, err = f.read.Relations(f.ctx, p, InviteFilter{})
	nodeCode(t, err, httpx.ErrForbidden)
}
