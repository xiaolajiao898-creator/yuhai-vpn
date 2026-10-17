package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 风控处置的测试重点：三个能力必须分别授予、写操作必须二次验证、
// 解除是留痕而不是删除、同一目标不得同时有两条生效条目、
// 以及 effects 只接受代码真正会读的那三个值。

type riskFixture struct {
	*managedUserFixture
	writer *RiskWriteService
	read   *RiskReadService
}

func newRiskFixture(t *testing.T) *riskFixture {
	t.Helper()
	f := newManagedUserFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"blacklist", "auth_sessions"} {
		f.write(t, "CREATE TABLE "+pgx.Identifier{table}.Sanitize()+" (LIKE "+
			pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)")
	}

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00027_admin_risk_write.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	f.write(t, up)
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions
 WHERE code IN ('risk.blacklist_add','risk.blacklist_remove','users.status') AND risk_level=2`) != 3 {
		t.Fatal("风控写能力未定义")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='risk.read' AND risk_level=0`) != 1 {
		t.Fatal("risk.read 能力未定义")
	}
	f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{"blacklist"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT ON "+pgx.Identifier{"auth_sessions"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{"admin_risk_mutations"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT UPDATE(status,updated_at) ON "+pgx.Identifier{"users"}.Sanitize()+" TO "+role)
	f.write(t, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code)
 VALUES($1,'risk.read'),($1,'risk.blacklist_add'),($1,'risk.blacklist_remove'),($1,'users.status')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, "risk.read", actionBlacklistAdd,
			actionBlacklistRemove, actionUserStatus)
	}
	return &riskFixture{managedUserFixture: f,
		writer: &RiskWriteService{Security: f.svc}, read: &RiskReadService{Auth: f.svc.Auth}}
}

func (f *riskFixture) riskProof(t *testing.T, action, method, path string, body any) ApprovalProof {
	t.Helper()
	raw, _ := json.Marshal(body)
	p := f.proof(t, 0, action, path, string(raw))
	p.RequestHash = RequestHash(method, path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), p.RequestHash)
	return p
}

func blacklistInput() BlacklistAddInput {
	return BlacklistAddInput{OperationID: uuid.NewString(), TargetType: "USER", TargetValue: "12345",
		Effects: []string{"INVITE_BIND"}, Reason: "刷邀请奖励"}
}

func TestBlacklistAddRemoveKeepsHistoryIntegration(t *testing.T) {
	f := newRiskFixture(t)
	in := blacklistInput()
	entry, err := f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in), in)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != "ACTIVE" || entry.TargetValue != "12345" || len(entry.Effects) != 1 {
		t.Fatalf("新增结果不符：%+v", entry)
	}
	id, err := positiveApprovalID(entry.ID)
	if err != nil {
		t.Fatal(err)
	}

	// 同一目标不得再加第二条生效条目。
	dup := blacklistInput()
	_, err = f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", dup), dup)
	nodeCode(t, err, httpx.ErrConflict)

	// 解除是留痕，不是删除。
	rm := RiskReasonInput{OperationID: uuid.NewString(), Reason: "误判，恢复"}
	path := fmt.Sprintf("/admin/v1/blacklist/%d", id)
	if err = f.writer.RemoveBlacklist(f.ctx, f.riskProof(t, actionBlacklistRemove, "DELETE", path, rm), id, rm); err != nil {
		t.Fatal(err)
	}
	var status string
	var removedAt *time.Time
	var removedBy *int64
	if err = f.pool.QueryRow(f.ctx, `SELECT status,removed_at,removed_by FROM blacklist WHERE id=$1`, id).
		Scan(&status, &removedAt, &removedBy); err != nil {
		t.Fatal(err)
	}
	if status != "REMOVED" || removedAt == nil || removedBy == nil {
		t.Fatalf("解除未留痕：%s %v %v", status, removedAt, removedBy)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM blacklist`); n != 1 {
		t.Fatalf("解除把记录删掉了，剩 %d 条", n)
	}

	// 解除之后同一目标可以重新加。
	again := blacklistInput()
	if _, err = f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", again), again); err != nil {
		t.Fatalf("解除后应当可以重新加：%v", err)
	}
}

func TestBlacklistRejectsUnreadEffectsIntegration(t *testing.T) {
	f := newRiskFixture(t)
	// 代码里真正会检查的只有这三个值；写入别的等于给运维一个假的安全感。
	for _, bad := range [][]string{
		{"LOGIN"}, {"CONNECT"}, {"ALL", "ALL"}, {}, {"INVITE_BIND", "INVITE_REWARD", "ALL", "LOGIN"},
	} {
		in := blacklistInput()
		in.Effects = bad
		_, err := f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in), in)
		nodeCode(t, err, httpx.ErrBadRequest)
	}
	// 目标类型也按数据库实际允许的取值校验。
	for _, bad := range []string{"DEVICE_FINGERPRINT", "EMAIL", ""} {
		in := blacklistInput()
		in.TargetType = bad
		_, err := f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in), in)
		nodeCode(t, err, httpx.ErrBadRequest)
	}
	// 已经过期的截止时间等于一条从未生效的记录。
	in := blacklistInput()
	past := f.clock.Now().Add(-time.Second)
	in.ExpiresAt = &past
	_, err := f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in), in)
	nodeCode(t, err, httpx.ErrBadRequest)

	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM blacklist`); n != 0 {
		t.Fatalf("被拒的请求写进了 %d 条", n)
	}
}

func TestRiskWriteRequiresStepUpAndPermissionIntegration(t *testing.T) {
	f := newRiskFixture(t)
	in := blacklistInput()

	// 没有二次验证令牌。
	bare := f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in)
	bare.Token = ""
	_, err := f.writer.AddBlacklist(f.ctx, bare, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	// 令牌绑定的是另一个请求。
	wrong := f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in)
	wrong.RequestHash = RequestHash("POST", "/admin/v1/blacklist", []byte("{}"))
	_, err = f.writer.AddBlacklist(f.ctx, wrong, in)
	nodeCode(t, err, httpx.ErrStepUpInvalid)

	// 新增、解除、改账户状态是三个独立能力：撤掉「新增」不影响另外两个。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='risk.blacklist_add'`, f.role)
	in2 := blacklistInput()
	_, err = f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in2), in2)
	nodeCode(t, err, httpx.ErrForbidden)

	st := UserStatusInput{OperationID: uuid.NewString(), Status: "SUSPENDED", Reason: "配合调查"}
	path := fmt.Sprintf("/admin/v1/users/%d/status", f.userID)
	if err = f.writer.SetUserStatus(f.ctx, f.riskProof(t, actionUserStatus, "POST", path, st), f.userID, st); err != nil {
		t.Fatalf("users.status 不应受 risk.blacklist_add 影响：%v", err)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM blacklist`); n != 0 {
		t.Fatalf("被拒的请求写进了 %d 条", n)
	}
}

func TestUserStatusTransitionsAndIdempotencyIntegration(t *testing.T) {
	f := newRiskFixture(t)
	path := fmt.Sprintf("/admin/v1/users/%d/status", f.userID)

	for _, want := range []string{"SUSPENDED", "BANNED", "ACTIVE"} {
		in := UserStatusInput{OperationID: uuid.NewString(), Status: want, Reason: "风控处置：" + want}
		p := f.riskProof(t, actionUserStatus, "POST", path, in)
		if err := f.writer.SetUserStatus(f.ctx, p, f.userID, in); err != nil {
			t.Fatalf("设置 %s 失败：%v", want, err)
		}
		var got string
		if err := f.pool.QueryRow(f.ctx, `SELECT status FROM users WHERE id=$1`, f.userID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("状态未生效：期望 %s 实际 %s", want, got)
		}
		// 同一 operation_id 重复提交是幂等的。
		if err := f.writer.SetUserStatus(f.ctx, p, f.userID, in); err != nil {
			t.Fatalf("重复提交应当幂等：%v", err)
		}
	}

	// 停用账户**不吊销登录会话**——这是有意的，SUSPENDED 下用户仍应能看到自己的账户。
	f.write(t, `INSERT INTO auth_sessions(user_id,device_id,refresh_hash,family_id,secret_version,expires_at)
 SELECT $1,d.id,$2,$3,1,$4 FROM devices d WHERE d.user_id=$1 LIMIT 1`,
		f.userID, strings.Repeat("f", 64), uuid.NewString(), f.clock.Now().Add(time.Hour))
	susp := UserStatusInput{OperationID: uuid.NewString(), Status: "SUSPENDED", Reason: "再次停用"}
	if err := f.writer.SetUserStatus(f.ctx, f.riskProof(t, actionUserStatus, "POST", path, susp), f.userID, susp); err != nil {
		t.Fatal(err)
	}
	var live int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM auth_sessions
 WHERE user_id=$1 AND revoked_at IS NULL`, f.userID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live == 0 {
		t.Fatal("停用账户吊销了登录会话——与契约声明不符")
	}

	// 非法状态与不存在的用户。
	bad := UserStatusInput{OperationID: uuid.NewString(), Status: "DELETED", Reason: "非法状态"}
	nodeCode(t, f.writer.SetUserStatus(f.ctx, f.riskProof(t, actionUserStatus, "POST", path, bad), f.userID, bad),
		httpx.ErrBadRequest)
	missing := UserStatusInput{OperationID: uuid.NewString(), Status: "BANNED", Reason: "不存在的用户"}
	mp := f.riskProof(t, actionUserStatus, "POST", "/admin/v1/users/999999/status", missing)
	nodeCode(t, f.writer.SetUserStatus(f.ctx, mp, 999999, missing), httpx.ErrNotFound)
}

func TestRiskConcurrentSameOperationAppliesOnceIntegration(t *testing.T) {
	f := newRiskFixture(t)
	in := blacklistInput()
	p := f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in)

	const attempts = 6
	outs := make([]ManagedBlacklistEntry, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = f.writer.AddBlacklist(f.ctx, p, in)
		}(i)
	}
	wg.Wait()
	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			if outs[i].ID != outs[0].ID && outs[0].ID != "" {
				t.Fatalf("并发返回了不同条目：%s ≠ %s", outs[i].ID, outs[0].ID)
			}
		}
	}
	if success == 0 {
		t.Fatal("并发提交全部失败")
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM blacklist`); n != 1 {
		t.Fatalf("并发提交建出了 %d 条", n)
	}
}

func TestBlacklistListFiltersAndAuditIntegration(t *testing.T) {
	f := newRiskFixture(t)
	in := blacklistInput()
	entry, err := f.writer.AddBlacklist(f.ctx, f.riskProof(t, actionBlacklistAdd, "POST", "/admin/v1/blacklist", in), in)
	if err != nil {
		t.Fatal(err)
	}
	p := f.people[0]

	page, err := f.read.Blacklist(f.ctx, p, FinanceFilter{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != entry.ID {
		t.Fatalf("列表不符：%+v", page.Items)
	}
	filtered, err := f.read.Blacklist(f.ctx, p, FinanceFilter{Status: "REMOVED"}, "")
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("状态筛选不符：%v %+v", err, filtered.Items)
	}
	byType, err := f.read.Blacklist(f.ctx, p, FinanceFilter{}, "IP_PREFIX")
	if err != nil || len(byType.Items) != 0 {
		t.Fatalf("类型筛选不符：%v %+v", err, byType.Items)
	}
	if _, err = f.read.Blacklist(f.ctx, p, FinanceFilter{}, "NOPE"); err == nil {
		t.Fatal("非法类型被接受了")
	}

	// 撤掉 risk.read 后不得再读，哪怕还有 risk.write。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='risk.read'`, f.role)
	_, err = f.read.Blacklist(f.ctx, p, FinanceFilter{}, "")
	nodeCode(t, err, httpx.ErrForbidden)

	// 审计必须记到，且标记做过二次验证。
	var action, result string
	var stepUp bool
	if err = f.pool.QueryRow(f.ctx, `SELECT action_code,result,step_up_verified FROM admin_audit_logs
 WHERE action_code=$1 ORDER BY id DESC LIMIT 1`, actionBlacklistAdd).Scan(&action, &result, &stepUp); err != nil {
		t.Fatal(err)
	}
	if result != "SUCCESS" || !stepUp {
		t.Fatalf("审计记录不符：%s %s %v", action, result, stepUp)
	}
}
