package admin

import (
	"encoding/base64"
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

// 注册令牌是「哪台机器可以代表这个节点」的钥匙，所以测试重点是：
// 必须二次验证、必须独立授权、明文只出现一次且不可重放、一个节点同时只有一把有效令牌。

type nodeEnrollFixture struct {
	*nodeWriteFixture
	enroll *NodeEnrollService
	nodeID int64
}

func newNodeEnrollFixture(t *testing.T) *nodeEnrollFixture {
	t.Helper()
	f := newNodeWriteFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, "node_enroll_tokens"}.Sanitize()+
		" (LIKE "+pgx.Identifier{"public", "node_enroll_tokens"}.Sanitize()+" INCLUDING ALL)")
	f.write(t, "CREATE TABLE "+pgx.Identifier{f.schema, "node_enrollments"}.Sanitize()+
		" (LIKE "+pgx.Identifier{"public", "node_enrollments"}.Sanitize()+" INCLUDING ALL)")

	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00024_node_enrollment.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	// node_enrollments 已按 public 结构建好，迁移里重复建表会冲突，只取权限与幂等表两段。
	for _, stmt := range strings.Split(up, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" || strings.Contains(s, "CREATE TABLE node_enrollments") ||
			strings.Contains(s, "CREATE INDEX node_enrollments_node_idx") {
			continue
		}
		f.write(t, s)
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code IN ('nodes.enroll_token','nodes.revoke') AND risk_level=2`) != 2 {
		t.Fatal("节点身份能力未定义")
	}
	for _, table := range []string{"node_enroll_tokens", "node_enrollments", "admin_node_security_mutations"} {
		f.write(t, "GRANT SELECT,INSERT,UPDATE ON "+pgx.Identifier{f.schema, table}.Sanitize()+" TO "+role)
	}
	f.write(t, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" TO "+role)
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'nodes.enroll_token'),($1,'nodes.revoke')`, f.role)
	for i := range f.people {
		f.people[i].Permissions = append(f.people[i].Permissions, actionEnrollToken, actionNodeRevoke)
	}
	f.node(t, 500, "注册节点", "MAINTENANCE", "JP", nil)
	return &nodeEnrollFixture{nodeWriteFixture: f, enroll: &NodeEnrollService{Security: f.svc}, nodeID: 500}
}

// securityProof 造一个绑定到具体请求的二次验证凭证，与 device_revoke 的做法一致。
func (f *nodeEnrollFixture) securityProof(t *testing.T, action, path string, in NodeSecurityInput) ApprovalProof {
	t.Helper()
	raw, _ := json.Marshal(in)
	p := f.proof(t, 0, action, path, string(raw))
	p.RequestHash = RequestHash("POST", path, raw)
	f.write(t, `UPDATE admin_step_up_tokens SET payload_hash=$2 WHERE token_hash=$1`, tokenHash(p.Token), p.RequestHash)
	return p
}

func (f *nodeEnrollFixture) issueInput() NodeSecurityInput {
	return NodeSecurityInput{OperationID: uuid.NewString(), Reason: "部署新节点，签发注册令牌"}
}

func (f *nodeEnrollFixture) tokenPath() string {
	return fmt.Sprintf("/admin/v1/nodes/%d/enroll-tokens", f.nodeID)
}

func TestEnrollTokenIssuedOnceAndHashedIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	in := f.issueInput()
	p := f.securityProof(t, actionEnrollToken, f.tokenPath(), in)

	out, err := f.enroll.IssueEnrollToken(f.ctx, p, f.nodeID, in)
	if err != nil {
		t.Fatal(err)
	}
	// 令牌必须是 32 字节随机的 Base64URL 无填充写法。
	if len(out.Token) != 43 {
		t.Fatalf("令牌长度不符：%d", len(out.Token))
	}
	if b, e := base64.RawURLEncoding.DecodeString(out.Token); e != nil || len(b) != 32 {
		t.Fatalf("令牌不是 32 字节随机值：%v", e)
	}
	if !out.ExpiresAt.Equal(f.clock.Now().Add(24 * time.Hour)) {
		t.Fatalf("有效期不符：%v", out.ExpiresAt)
	}

	// 库里只能有摘要，绝不能有明文。
	var stored string
	var used *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT token_hash,used_at FROM node_enroll_tokens WHERE node_id=$1`,
		f.nodeID).Scan(&stored, &used); err != nil {
		t.Fatal(err)
	}
	if stored != tokenHash(out.Token) || used != nil {
		t.Fatalf("落库的不是摘要或已被标记使用：%s", stored)
	}
	var plain int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM node_enroll_tokens WHERE token_hash=$1`,
		out.Token).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != 0 {
		t.Fatal("库里出现了令牌明文")
	}

	// 令牌不可重放：同一 operation_id 再提交必须 40901，而不是把旧令牌再给一次。
	_, err = f.enroll.IssueEnrollToken(f.ctx, p, f.nodeID, in)
	nodeCode(t, err, httpx.ErrConflict)
}

func TestEnrollTokenRequiresStepUpAndPermissionIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	in := f.issueInput()

	// 没有二次验证令牌。
	bare := f.securityProof(t, actionEnrollToken, f.tokenPath(), in)
	bare.Token = ""
	_, err := f.enroll.IssueEnrollToken(f.ctx, bare, f.nodeID, in)
	nodeCode(t, err, httpx.ErrStepUpRequired)

	// 二次验证令牌绑定的是另一个请求。
	wrong := f.securityProof(t, actionEnrollToken, f.tokenPath(), in)
	wrong.RequestHash = RequestHash("POST", "/admin/v1/nodes/999/enroll-tokens", []byte("{}"))
	_, err = f.enroll.IssueEnrollToken(f.ctx, wrong, f.nodeID, in)
	nodeCode(t, err, httpx.ErrStepUpInvalid)

	// 撤掉 nodes.enroll 授权后，即便二次验证有效也不得签发。
	f.write(t, `DELETE FROM admin_role_permissions WHERE role_id=$1 AND permission_code='nodes.enroll_token'`, f.role)
	in2 := f.issueInput()
	p := f.securityProof(t, actionEnrollToken, f.tokenPath(), in2)
	_, err = f.enroll.IssueEnrollToken(f.ctx, p, f.nodeID, in2)
	nodeCode(t, err, httpx.ErrForbidden)

	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_enroll_tokens`); n != 0 {
		t.Fatalf("被拒的请求仍然签发了 %d 把令牌", n)
	}
}

func TestEnrollTokenSupersedesPreviousIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	first := f.issueInput()
	out1, err := f.enroll.IssueEnrollToken(f.ctx, f.securityProof(t, actionEnrollToken, f.tokenPath(), first), f.nodeID, first)
	if err != nil {
		t.Fatal(err)
	}
	second := f.issueInput()
	out2, err := f.enroll.IssueEnrollToken(f.ctx, f.securityProof(t, actionEnrollToken, f.tokenPath(), second), f.nodeID, second)
	if err != nil {
		t.Fatal(err)
	}
	if out1.Token == out2.Token {
		t.Fatal("两次签发返回了同一把令牌")
	}
	// 旧令牌必须立刻过期，但不能被谎称「已使用」。
	var expires time.Time
	var used *time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT expires_at,used_at FROM node_enroll_tokens WHERE token_hash=$1`,
		tokenHash(out1.Token)).Scan(&expires, &used); err != nil {
		t.Fatal(err)
	}
	if expires.After(f.clock.Now()) {
		t.Fatal("旧令牌仍然有效")
	}
	if used != nil {
		t.Fatal("旧令牌被谎称已使用")
	}
	// 新令牌照常有效。
	if err := f.pool.QueryRow(f.ctx, `SELECT expires_at FROM node_enroll_tokens WHERE token_hash=$1`,
		tokenHash(out2.Token)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if !expires.After(f.clock.Now()) {
		t.Fatal("新令牌不该已过期")
	}
}

func TestEnrollTokenConcurrentSameOperationIssuesOnceIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	in := f.issueInput()
	p := f.securityProof(t, actionEnrollToken, f.tokenPath(), in)

	const attempts = 6
	outs := make([]EnrollTokenResult, attempts)
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = f.enroll.IssueEnrollToken(f.ctx, p, f.nodeID, in)
		}(i)
	}
	wg.Wait()

	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			if len(outs[i].Token) != 43 {
				t.Fatalf("成功的那次返回了无效令牌：%q", outs[i].Token)
			}
		}
	}
	// 令牌不可重放，所以并发里只能有一次成功，其余都是 40901 或二次验证被消费后的拒绝。
	if success != 1 {
		t.Fatalf("并发签发成功了 %d 次，应当只有 1 次", success)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_enroll_tokens`); n != 1 {
		t.Fatalf("并发签发建出了 %d 把令牌", n)
	}
}

func TestNodeRevokeClearsBindingAndTokensIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	// 先给节点一个证书绑定和一把未使用的令牌。
	f.write(t, `UPDATE nodes SET agent_cert_fingerprint=$1,status='ONLINE',
 agent_cert_prev_fingerprint=$3,agent_cert_prev_until=$4 WHERE id=$2`,
		strings.Repeat("a", 64), f.nodeID, strings.Repeat("b", 64), f.clock.Now().Add(24*time.Hour))
	issue := f.issueInput()
	token, err := f.enroll.IssueEnrollToken(f.ctx, f.securityProof(t, actionEnrollToken, f.tokenPath(), issue), f.nodeID, issue)
	if err != nil {
		t.Fatal(err)
	}

	path := fmt.Sprintf("/admin/v1/nodes/%d/revoke", f.nodeID)
	in := NodeSecurityInput{OperationID: uuid.NewString(), Reason: "节点被入侵，立即解除绑定"}
	p := f.securityProof(t, actionNodeRevoke, path, in)
	if err := f.enroll.RevokeNode(f.ctx, p, f.nodeID, in); err != nil {
		t.Fatal(err)
	}

	var pin, previous *string
	var previousUntil *time.Time
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint,status,
 agent_cert_prev_fingerprint,agent_cert_prev_until FROM nodes WHERE id=$1`,
		f.nodeID).Scan(&pin, &status, &previous, &previousUntil); err != nil {
		t.Fatal(err)
	}
	if pin != nil || previous != nil || previousUntil != nil || status != "DISABLED" {
		t.Fatalf("吊销后仍有绑定或未下线：%v %s", pin, status)
	}
	// 未使用的令牌必须一并作废，否则还能用旧令牌换回一张新证书。
	var expires time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT expires_at FROM node_enroll_tokens WHERE token_hash=$1`,
		tokenHash(token.Token)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires.After(f.clock.Now()) {
		t.Fatal("吊销后注册令牌仍然有效")
	}

	// 幂等：同一 operation_id 重复提交仍然成功。
	if err := f.enroll.RevokeNode(f.ctx, p, f.nodeID, in); err != nil {
		t.Fatalf("重复吊销应当幂等：%v", err)
	}
	// 节点不存在。
	missing := NodeSecurityInput{OperationID: uuid.NewString(), Reason: "不存在的节点"}
	mp := f.securityProof(t, actionNodeRevoke, "/admin/v1/nodes/999999/revoke", missing)
	nodeCode(t, f.enroll.RevokeNode(f.ctx, mp, 999999, missing), httpx.ErrNotFound)
}

func TestNodeSecurityAuditTrailIntegration(t *testing.T) {
	f := newNodeEnrollFixture(t)
	in := f.issueInput()
	if _, err := f.enroll.IssueEnrollToken(f.ctx, f.securityProof(t, actionEnrollToken, f.tokenPath(), in), f.nodeID, in); err != nil {
		t.Fatal(err)
	}
	var action, result string
	var stepUp bool
	var after []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT action_code,result,step_up_verified,after_data
 FROM admin_audit_logs ORDER BY id DESC LIMIT 1`).Scan(&action, &result, &stepUp, &after); err != nil {
		t.Fatal(err)
	}
	if action != actionEnrollToken || result != "SUCCESS" || !stepUp {
		t.Fatalf("审计记录不符：%s %s %v", action, result, stepUp)
	}
	// 审计里绝不能出现令牌本身。
	if strings.Contains(string(after), "token") {
		t.Fatalf("审计快照疑似包含令牌：%s", after)
	}
}
