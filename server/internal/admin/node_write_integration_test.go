package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 节点写接口的测试重点：权限必须单独授予、幂等键必须真的幂等、
// 入站参数必须在**建的时候**就挡住（而不是等下发时才发现整台节点资料无效）、
// 以及这些接口绝不能碰 agent_cert_fingerprint。

type nodeWriteFixture struct {
	*managedNodeFixture
	writer     *NodeWriteService
	plainProof ApprovalProof
}

func newNodeWriteFixture(t *testing.T) *nodeWriteFixture {
	t.Helper()
	f := newManagedNodeFixture(t)
	before := authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`)
	raw, err := os.ReadFile("../db/migrations/00023_admin_node_write.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, strings.Split(string(raw), "-- +goose Down")[0])
	// 迁移只定义能力，不得顺手授予任何角色。
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_role_permissions`) != before {
		t.Fatal("迁移擅自授予了权限")
	}
	if authCount(t, f.svc.Auth, `SELECT count(*) FROM admin_permissions WHERE code='nodes.write' AND risk_level=1`) != 1 {
		t.Fatal("nodes.write 能力未定义")
	}
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	for _, table := range []string{"node_groups", "nodes", "node_inbounds"} {
		f.write(t, "GRANT INSERT,UPDATE ON "+pgx.Identifier{f.schema, table}.Sanitize()+" TO "+role)
	}
	f.write(t, "GRANT SELECT,INSERT ON "+pgx.Identifier{f.schema, "admin_node_mutations"}.Sanitize()+" TO "+role)
	for _, table := range []string{"node_groups", "nodes", "node_inbounds"} {
		f.write(t, "GRANT USAGE,SELECT ON SEQUENCE "+pgx.Identifier{f.schema, table + "_id_seq"}.Sanitize()+" TO "+role)
	}

	w := &nodeWriteFixture{managedNodeFixture: f, writer: &NodeWriteService{Auth: f.svc.Auth, Audit: f.audit}}
	w.plainProof = ApprovalProof{Principal: f.people[0], RequestID: strings.Repeat("r", 16), IP: "203.0.113.9", UserAgent: "fixture"}
	return w
}

// grantWrite 单独授予 nodes.write——默认不授予，正是要测「没授权就必须被拒」。
func (f *nodeWriteFixture) grantWrite(t *testing.T) {
	t.Helper()
	f.write(t, `INSERT INTO admin_role_permissions(role_id,permission_code) VALUES($1,'nodes.write')`, f.role)
}

func (f *nodeWriteFixture) withHash(hash string) ApprovalProof {
	p := f.plainProof
	p.RequestHash = hash
	return p
}

func hash64(seed string) string {
	out := ""
	for len(out) < 64 {
		out += fmt.Sprintf("%x", seed)
	}
	return out[:64]
}

func wgParams(key string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"1","dns":["10.20.0.1"],"mtu":1280}`, key))
}

const nodeWriteKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func nodeCode(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var coded *httpx.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("期望错误码 %d，实际 %v", want, err)
	}
}

func TestNodeWriteRequiresItsOwnPermissionIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	tier := 1
	in := NodeGroupCreate{OperationID: uuid.NewString(), Name: "新线路", MinTier: &tier}

	// 只有 nodes.read 的会话不得写——这是两个独立的能力。
	_, err := f.writer.CreateGroup(f.ctx, f.withHash(hash64("a")), in)
	nodeCode(t, err, httpx.ErrForbidden)

	f.grantWrite(t)
	if _, err = f.writer.CreateGroup(f.ctx, f.withHash(hash64("a")), in); err != nil {
		t.Fatal(err)
	}

	// 会话被吊销后立刻失效，哪怕权限还在。
	f.write(t, `UPDATE admin_sessions SET revoked_at=$1 WHERE id=$2`, f.clock.Now(), f.people[0].SessionID)
	in.OperationID = uuid.NewString()
	_, err = f.writer.CreateGroup(f.ctx, f.withHash(hash64("b")), in)
	nodeCode(t, err, httpx.ErrForbidden)
}

func TestNodeWriteIdempotencyIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	tier := 2
	op := uuid.NewString()
	in := NodeGroupCreate{OperationID: op, Name: "幂等线路", MinTier: &tier}

	first, err := f.writer.CreateGroup(f.ctx, f.withHash(hash64("a")), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.writer.CreateGroup(f.ctx, f.withHash(hash64("a")), in)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("同一 operation_id 返回了不同结果：%s ≠ %s", first, second)
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_groups WHERE name='幂等线路'`); n != 1 {
		t.Fatalf("重复提交建出了 %d 个分组", n)
	}

	// 同一 operation_id 换了请求体：必须 40901，而不是悄悄返回旧结果。
	in.Name = "换了名字"
	_, err = f.writer.CreateGroup(f.ctx, f.withHash(hash64("c")), in)
	nodeCode(t, err, httpx.ErrConflict)
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_groups WHERE name='换了名字'`); n != 0 {
		t.Fatal("请求体不一致时仍然写了库")
	}

	// 同一 operation_id 用在另一种操作上同样拒绝。
	_, err = f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: op, GroupID: "1",
		Name: "n", CountryCode: "HK", City: "香港", Region: "ASIA", Host: "a.example.net"})
	nodeCode(t, err, httpx.ErrConflict)
}

func TestNodeWriteConcurrentSameOperationCreatesOnceIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	tier := 1
	in := NodeGroupCreate{OperationID: uuid.NewString(), Name: "并发线路", MinTier: &tier}

	const attempts = 6
	results := make([]string, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(attempts)
	done.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			out, err := f.writer.CreateGroup(f.ctx, f.withHash(hash64("a")), in)
			results[i], errs[i] = string(out), err
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 次并发提交失败：%v", i, err)
		}
		if results[i] != results[0] {
			t.Fatalf("并发提交返回了不同结果：%s ≠ %s", results[i], results[0])
		}
	}
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_groups WHERE name='并发线路'`); n != 1 {
		t.Fatalf("并发提交建出了 %d 个分组", n)
	}
}

func TestNodeCreateAlwaysStartsInMaintenanceIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	raw, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "东京 01", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	var n ManagedNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	// 没有证书、没有心跳，就不能装成已上线。
	if n.Status != "MAINTENANCE" {
		t.Fatalf("新建节点状态应为 MAINTENANCE，实际 %s", n.Status)
	}
	if n.HeartbeatState != "NEVER" || n.LastHeartbeatAt != nil {
		t.Fatalf("新建节点不该有心跳：%s %v", n.HeartbeatState, n.LastHeartbeatAt)
	}
	// 证书指纹只能由注册流程写入，这条路径必须留空。
	var pin *string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint FROM nodes WHERE id::text=$1`, n.ID).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin != nil {
		t.Fatalf("新建节点被写入了证书指纹：%s", *pin)
	}

	// 分组不存在时拒绝，而不是建出一个挂在空分组上的节点。
	_, err = f.writer.CreateNode(f.ctx, f.withHash(hash64("b")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "999999", Name: "孤儿", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "x.example.net"})
	nodeCode(t, err, httpx.ErrNotFound)
	if n := authCount(t, f.svc.Auth, `SELECT count(*) FROM nodes WHERE name='孤儿'`); n != 0 {
		t.Fatal("分组不存在却建出了节点")
	}
}

func TestNodeUpdateOnlyTouchesGivenFieldsIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	raw, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "原名", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	var created ManagedNode
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	id, err := positiveApprovalID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 给这台节点一个证书指纹，模拟它已经注册过。
	f.write(t, `UPDATE nodes SET agent_cert_fingerprint=$1 WHERE id=$2`, strings.Repeat("a", 64), id)

	name := "改名后"
	raw, err = f.writer.UpdateNode(f.ctx, f.withHash(hash64("b")), id, NodeUpdate{OperationID: uuid.NewString(), Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	var updated ManagedNode
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != name {
		t.Fatalf("名称未更新：%s", updated.Name)
	}
	// 没给的字段一个都不能变。
	if updated.CountryCode != created.CountryCode || updated.City != created.City ||
		updated.Region != created.Region || updated.Status != created.Status ||
		updated.CapacityUsers != created.CapacityUsers || updated.Group.ID != created.Group.ID {
		t.Fatalf("未指定的字段被改动了：%+v → %+v", created, updated)
	}
	// 证书指纹更不能被这条路径动到。
	var pin string
	if err := f.pool.QueryRow(f.ctx, `SELECT agent_cert_fingerprint FROM nodes WHERE id=$1`, id).Scan(&pin); err != nil {
		t.Fatal(err)
	}
	if pin != strings.Repeat("a", 64) {
		t.Fatalf("证书指纹被修改了：%s", pin)
	}

	// 空修改要明确拒绝，不能当成一次「成功的无操作」。
	_, err = f.writer.UpdateNode(f.ctx, f.withHash(hash64("c")), id, NodeUpdate{OperationID: uuid.NewString()})
	nodeCode(t, err, httpx.ErrBadRequest)

	// 节点不存在。
	_, err = f.writer.UpdateNode(f.ctx, f.withHash(hash64("d")), 999999, NodeUpdate{OperationID: uuid.NewString(), Name: &name})
	nodeCode(t, err, httpx.ErrNotFound)

	// 改到不存在的分组要拒绝，而不是把节点挂空。
	missing := "999999"
	_, err = f.writer.UpdateNode(f.ctx, f.withHash(hash64("e")), id, NodeUpdate{OperationID: uuid.NewString(), GroupID: &missing})
	nodeCode(t, err, httpx.ErrNotFound)
	if g := authCount(t, f.svc.Auth, `SELECT count(*) FROM nodes WHERE id=`+created.ID+` AND group_id=1`); g != 1 {
		t.Fatal("分组不存在时节点的分组被改动了")
	}
}

func TestInboundParamsRejectedAtCreationIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	raw, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "入站节点", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	var n ManagedNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(n.ID)
	port := 51820

	// 每一条都必须在**建的时候**就被挡住——放进库里就会让整台节点的用户拿不到材料。
	for name, params := range map[string]string{
		"未知字段":     `{"interface":"wg0","public_key":"` + nodeWriteKey + `","config_version":"1","private_key":"x"}`,
		"缺接口":      `{"public_key":"` + nodeWriteKey + `","config_version":"1"}`,
		"接口名非法":    `{"interface":"lo","public_key":"` + nodeWriteKey + `","config_version":"1"}`,
		"公钥全零":     `{"interface":"wg0","public_key":"` + strings.Repeat("A", 43) + `=","config_version":"1"}`,
		"公钥非规范":    `{"interface":"wg0","public_key":"not-base64","config_version":"1"}`,
		"版本为零":     `{"interface":"wg0","public_key":"` + nodeWriteKey + `","config_version":"0"}`,
		"版本前导零":    `{"interface":"wg0","public_key":"` + nodeWriteKey + `","config_version":"01"}`,
		"DNS 是公网":  `{"interface":"wg0","public_key":"` + nodeWriteKey + `","config_version":"1","dns":["8.8.8.8"]}`,
		"MTU 过小":   `{"interface":"wg0","public_key":"` + nodeWriteKey + `","config_version":"1","mtu":1000}`,
		"重复键":      `{"interface":"wg0","interface":"wg1","public_key":"` + nodeWriteKey + `","config_version":"1"}`,
		"null 必填项": `{"interface":null,"public_key":"` + nodeWriteKey + `","config_version":"1"}`,
		"不是对象":     `["wg0"]`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.writer.CreateInbound(f.ctx, f.withHash(hash64(name)), id, InboundCreate{
				OperationID: uuid.NewString(), Protocol: "WIREGUARD", Port: &port,
				PublicParams: json.RawMessage(params)})
			nodeCode(t, err, httpx.ErrBadRequest)
		})
	}
	if c := authCount(t, f.svc.Auth, `SELECT count(*) FROM node_inbounds`); c != 0 {
		t.Fatalf("非法参数进了库：%d 条", c)
	}

	// 尚未支持的协议宁可明说，也不存未经校验的参数。
	_, err = f.writer.CreateInbound(f.ctx, f.withHash(hash64("z")), id, InboundCreate{
		OperationID: uuid.NewString(), Protocol: "TROJAN", Port: &port, PublicParams: wgParams(nodeWriteKey)})
	nodeCode(t, err, httpx.ErrBadRequest)
}

func TestInboundCreateDefaultsAndConflictIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	raw, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "入站节点", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	var n ManagedNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(n.ID)
	port := 51820

	raw, err = f.writer.CreateInbound(f.ctx, f.withHash(hash64("b")), id, InboundCreate{
		OperationID: uuid.NewString(), Protocol: "WIREGUARD", Port: &port, PublicParams: wgParams(nodeWriteKey)})
	if err != nil {
		t.Fatal(err)
	}
	var v ManagedInboundDetail
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	// 默认 DISABLED：新建入站不该在管理员核对之前就对用户生效。
	if v.Status != "DISABLED" || v.Priority != 100 || v.Port != port {
		t.Fatalf("默认值不符：%+v", v)
	}
	// 存进去的是规范化后的那份，不是调用方原样的字节。
	var stored map[string]any
	if err := json.Unmarshal(v.PublicParams, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["public_key"] != nodeWriteKey || stored["interface"] != "wg0" || len(stored) != 5 {
		t.Fatalf("落库参数不符：%v", stored)
	}

	// 同节点同协议同端口只能有一条。
	_, err = f.writer.CreateInbound(f.ctx, f.withHash(hash64("c")), id, InboundCreate{
		OperationID: uuid.NewString(), Protocol: "WIREGUARD", Port: &port, PublicParams: wgParams(nodeWriteKey)})
	nodeCode(t, err, httpx.ErrConflict)

	// 节点不存在。
	_, err = f.writer.CreateInbound(f.ctx, f.withHash(hash64("d")), 999999, InboundCreate{
		OperationID: uuid.NewString(), Protocol: "WIREGUARD", Port: &port, PublicParams: wgParams(nodeWriteKey)})
	nodeCode(t, err, httpx.ErrNotFound)
}

func TestNodeGroupAndInboundListsIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	p := f.people[0]

	groups, err := f.service.Groups(f.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.Items) != 1 || groups.Items[0].Name != "标准线路" {
		t.Fatalf("分组列表不符：%+v", groups.Items)
	}

	raw, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "列表节点", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	var n ManagedNode
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatal(err)
	}
	id, _ := positiveApprovalID(n.ID)

	empty, err := f.service.Inbounds(f.ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("新节点不该有入站：%+v", empty.Items)
	}
	port := 51820
	if _, err = f.writer.CreateInbound(f.ctx, f.withHash(hash64("b")), id, InboundCreate{
		OperationID: uuid.NewString(), Protocol: "WIREGUARD", Port: &port, PublicParams: wgParams(nodeWriteKey)}); err != nil {
		t.Fatal(err)
	}
	list, err := f.service.Inbounds(f.ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Protocol != "WIREGUARD" {
		t.Fatalf("入站列表不符：%+v", list.Items)
	}
	// public_params 是公开材料，可以返回；但绝不能出现任何私钥字样。
	blob, _ := json.Marshal(list)
	for _, secret := range []string{"private_key", "secret", "proxy_uuid"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("入站列表出现了 %q", secret)
		}
	}

	// 节点不存在时列表也要 40401，而不是返回空列表让人以为节点存在但没入站。
	_, err = f.service.Inbounds(f.ctx, p, 999999)
	nodeCode(t, err, httpx.ErrNotFound)
}

func TestNodeWriteAuditTrailIntegration(t *testing.T) {
	f := newNodeWriteFixture(t)
	f.grantWrite(t)
	if _, err := f.writer.CreateNode(f.ctx, f.withHash(hash64("a")), NodeCreate{OperationID: uuid.NewString(),
		GroupID: "1", Name: "审计节点", CountryCode: "JP", City: "东京", Region: "ASIA", Host: "jp01.example.net"}); err != nil {
		t.Fatal(err)
	}
	var action, result, target string
	if err := f.pool.QueryRow(f.ctx, `SELECT action_code,result,target_type FROM admin_audit_logs ORDER BY id DESC LIMIT 1`).
		Scan(&action, &result, &target); err != nil {
		t.Fatal(err)
	}
	if action != "nodes.create" || result != "SUCCESS" || target != "node" {
		t.Fatalf("审计记录不符：%s %s %s", action, result, target)
	}
	// 审计链必须仍然自洽。
	logs, err := f.audit.List(f.ctx, 1, 50, "", "")
	if err != nil || len(logs.Items) == 0 {
		t.Fatal(err)
	}
}
