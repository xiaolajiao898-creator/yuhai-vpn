package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 连接材料是唯一会把「能连上」所需内容交给客户端的接口，所以测试重点全在拒绝面：
// 权益不够不给、设备被移除不给、节点没就绪不给、别的设备的材料绝不给。

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

type fixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	owner  *pgxpool.Pool
	clock  *testClock
	svc    *Service
	userID int64
	device int64
}

const wgNodeKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
const devicePubKey = "IiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/QEE="

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn, ownerDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL")
	if dsn == "" || ownerDSN == "" {
		t.Skip("需要 TEST_DATABASE_URL 与 TEST_MIGRATE_DATABASE_URL 指向隔离测试库")
	}
	app, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	if app.ConnConfig.User == admin.ConnConfig.User || app.ConnConfig.Database != admin.ConnConfig.Database {
		t.Fatal("需要同一隔离库上的两个不同角色")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.NewWithConfig(ctx, admin.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	schema := "connect_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	q := pgx.Identifier{schema}.Sanitize()
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+q); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := base.Exec(c, "DROP SCHEMA "+q+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, table := range []string{"users", "devices", "device_credentials", "subscriptions", "traffic_packs",
		"node_groups", "nodes", "node_inbounds", "node_heartbeats", "egress_endpoints"} {
		if _, err := base.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+
			" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	role := pgx.Identifier{app.ConnConfig.User}.Sanitize()
	// 授权必须写死在本测试 schema 上：base 连接的 search_path 是 public，
	// 不限定就会把权限授到生产表上，既有安全风险，也会让本包的最小权限断言失真。
	tbl := func(name string) string { return pgx.Identifier{schema, name}.Sanitize() }
	for _, sql := range []string{
		"REVOKE ALL ON ALL TABLES IN SCHEMA " + q + " FROM " + role,
		"GRANT USAGE ON SCHEMA " + q + " TO " + role,
		// 只授予本接口真正需要读的列；口令密文一列都不授权。
		"GRANT SELECT(id,status) ON " + tbl("users") + " TO " + role,
		"GRANT SELECT(id,user_id,status) ON " + tbl("devices") + " TO " + role,
		"GRANT SELECT(id,device_id,status,wg_pubkey,wg_address) ON " + tbl("device_credentials") + " TO " + role,
		"GRANT SELECT(user_id,tier,expire_at,period_quota_bytes,period_used_bytes) ON " + tbl("subscriptions") + " TO " + role,
		"GRANT SELECT(user_id,tier,status,expire_at,used_bytes,total_bytes) ON " + tbl("traffic_packs") + " TO " + role,
		"GRANT SELECT ON " + tbl("node_groups") + "," + tbl("nodes") + "," + tbl("node_inbounds") + "," + tbl("node_heartbeats") + " TO " + role,
		"GRANT SELECT ON " + tbl("egress_endpoints") + " TO " + role,
	} {
		if _, err := base.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	admin.ConnConfig.RuntimeParams["search_path"] = schema
	owner, err := pgxpool.NewWithConfig(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	app.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clk := &testClock{now: time.Date(2032, 1, 2, 3, 4, 5, 0, time.UTC)}
	f := &fixture{ctx: ctx, pool: pool, owner: owner, clock: clk,
		svc: &Service{Pool: pool, Clock: clk}, userID: 11, device: 21}
	f.exec(t, `INSERT INTO users(id,account_no,secret_lookup,invite_code,status,created_at,updated_at)
 OVERRIDING SYSTEM VALUE VALUES($1,'1234567890',$2,'INV11','ACTIVE',$3,$3)`, f.userID, strings.Repeat("a", 64), clk.now)
	f.exec(t, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform,status,created_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,'device-signing-key','ANDROID','ACTIVE',$4)`, f.device, f.userID, uuid.NewString(), clk.now)
	f.exec(t, `INSERT INTO device_credentials(id,device_id,proxy_uuid,proxy_secret_enc,wg_pubkey,wg_address,status)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5,'10.20.0.2/32'::inet,'ACTIVE')`,
		f.device, f.device, uuid.NewString(), []byte("private-secret-sentinel"), devicePubKey)
	f.exec(t, `INSERT INTO node_groups(id,name,min_tier) OVERRIDING SYSTEM VALUE VALUES(1,'标准',1)`)
	return f
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

// node 建一个具备完整就绪条件的节点。
func (f *fixture) node(t *testing.T, id int64, minTier int, params string) {
	t.Helper()
	pin := strings.Repeat("b", 64)
	f.exec(t, `INSERT INTO node_groups(id,name,min_tier) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3)
 ON CONFLICT(id) DO NOTHING`, int64(minTier+100), fmt.Sprintf("组%d", minTier), minTier)
	f.exec(t, `INSERT INTO nodes(id,group_id,name,country_code,city,region,host,probe_port,status,capacity_users,
 agent_cert_fingerprint,last_heartbeat_at,updated_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,'东京 01','JP','Tokyo','ASIA','jp01.example.net',443,'ONLINE',100,$3,$4,$4)`,
		id, int64(minTier+100), pin, f.clock.now)
	f.exec(t, `INSERT INTO node_heartbeats(node_id,sequence,payload_hash,sent_at,received_at,config_version,
 snapshot_checksum,cpu,mem,net_rx_bps,net_tx_bps,cert_fingerprint,ready_protocols)
 VALUES($1,1,$2,$3,$3,7,$4,10,10,0,0,$5,'["WIREGUARD"]'::jsonb)`,
		id, strings.Repeat("c", 64), f.clock.now, strings.Repeat("c", 64), pin)
	if params != "" {
		f.exec(t, `INSERT INTO node_inbounds(node_id,protocol,port,public_params,status,priority)
 VALUES($1,'WIREGUARD',51820,$2::jsonb,'ENABLED',1)`, id, params)
	}
}

func wgParamsJSON() string {
	return fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"7","dns":["10.20.0.1"],"mtu":1280}`, wgNodeKey)
}

func (f *fixture) plan(t *testing.T, tier int, quota *int64, used int64) {
	t.Helper()
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,device_limit,expire_at,period_quota_bytes,period_used_bytes)
 VALUES($1,$2,3,$3,$4,$5) ON CONFLICT(user_id) DO UPDATE SET tier=EXCLUDED.tier,expire_at=EXCLUDED.expire_at,
 period_quota_bytes=EXCLUDED.period_quota_bytes,period_used_bytes=EXCLUDED.period_used_bytes`,
		f.userID, tier, f.clock.now.Add(24*time.Hour), quota, used)
}

func code(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var coded *httpx.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("期望错误码 %d，实际 %v", want, err)
	}
}

func request(ids ...string) Input {
	return Input{NodeIDs: ids, Protocols: []string{"WIREGUARD"}}
}

func TestConnectProfilesAssemblesWireGuardIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())

	out, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Profiles) != 1 || len(out.Unavailable) != 0 {
		t.Fatalf("结果不符：%+v", out)
	}
	o := out.Profiles[0].Outbounds
	if len(o) != 1 || o[0].Protocol != "WIREGUARD" {
		t.Fatalf("出站不符：%+v", o)
	}
	if o[0].Endpoint != "jp01.example.net:51820" {
		t.Fatalf("端点不符：%s", o[0].Endpoint)
	}
	if o[0].ServerPublicKey != wgNodeKey {
		t.Fatalf("服务端公钥不符：%s", o[0].ServerPublicKey)
	}
	// 必须是**本设备自己**的隧道地址。
	if o[0].Address != "10.20.0.2/32" {
		t.Fatalf("下发了错误的隧道地址：%s", o[0].Address)
	}
	if len(o[0].DNS) != 1 || o[0].DNS[0] != "10.20.0.1" || o[0].MTU != 1280 {
		t.Fatalf("DNS/MTU 不符：%+v", o[0])
	}
	// 响应里不得出现任何密钥材料或口令密文。
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"private-secret-sentinel", "proxy_secret", devicePubKey} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("响应泄露了 %q", secret)
		}
	}
	if out.ExpiresAt == "" {
		t.Fatal("缺少有效期")
	}
}

func TestConnectProfilesNeverLeaksOtherDevicesMaterialIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())
	// 同一用户的第二台设备，地址不同。
	other := int64(22)
	f.exec(t, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform,status,created_at)
 OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,'k','IOS','ACTIVE',$4)`, other, f.userID, uuid.NewString(), f.clock.now)
	f.exec(t, `INSERT INTO device_credentials(id,device_id,proxy_uuid,proxy_secret_enc,wg_pubkey,wg_address,status)
 OVERRIDING SYSTEM VALUE VALUES($1,$1,$2,$3,$4,'10.20.0.99/32'::inet,'ACTIVE')`,
		other, uuid.NewString(), []byte("other-secret"), strings.Repeat("Z", 43)+"=")

	out, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Profiles[0].Outbounds[0].Address != "10.20.0.2/32" {
		t.Fatalf("下发了另一台设备的地址：%s", out.Profiles[0].Outbounds[0].Address)
	}
}

func TestConnectProfilesRefusesWithoutEntitlementIntegration(t *testing.T) {
	f := newFixture(t)
	f.node(t, 101, 1, wgParamsJSON())

	// 完全没有套餐。
	_, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrNoPlan)

	// 有套餐但流量已用完。
	quota := int64(100)
	f.plan(t, 1, &quota, 100)
	_, err = f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrQuotaExhausted)

	// 账户被停用。
	f.plan(t, 1, &quota, 0)
	f.exec(t, `UPDATE users SET status='SUSPENDED' WHERE id=$1`, f.userID)
	_, err = f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrAccountDisabled)
}

func TestConnectProfilesRefusesRemovedDeviceIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())

	f.exec(t, `UPDATE devices SET status='REVOKED' WHERE id=$1`, f.device)
	_, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrDeviceRevoked)

	// 别人的设备同样按已移除处理，不泄露它是否存在。
	f.exec(t, `UPDATE devices SET status='ACTIVE' WHERE id=$1`, f.device)
	_, err = f.svc.Profiles(f.ctx, 999, f.device, request("101"))
	if err == nil {
		t.Fatal("跨用户取材料成功了")
	}
}

func TestConnectProfilesMarksNodesUnavailableIntegration(t *testing.T) {
	// 每个用例都独占一个随机 schema，破坏节点 101，同时保留一个健康的节点 102，
	// 这样既能断言不可用原因，也能确认「一个节点坏了不影响其它节点下发」。
	cases := []struct {
		name     string
		break101 func(t *testing.T, f *fixture)
		reason   string
	}{
		{"等级不足", func(t *testing.T, f *fixture) {
			f.exec(t, `UPDATE node_groups SET min_tier=9 WHERE id=101`)
		}, "TIER"},
		{"维护中", func(t *testing.T, f *fixture) {
			f.exec(t, `UPDATE nodes SET status='MAINTENANCE' WHERE id=101`)
		}, "MAINTENANCE"},
		{"心跳陈旧", func(t *testing.T, f *fixture) {
			f.exec(t, `UPDATE nodes SET last_heartbeat_at=$1 WHERE id=101`, f.clock.now.Add(-2*time.Minute))
			f.exec(t, `UPDATE node_heartbeats SET received_at=$1,sent_at=$1 WHERE node_id=101`, f.clock.now.Add(-2*time.Minute))
		}, "OFFLINE"},
		{"证书指纹不一致", func(t *testing.T, f *fixture) {
			f.exec(t, `UPDATE node_heartbeats SET cert_fingerprint=$1 WHERE node_id=101`, strings.Repeat("d", 64))
		}, "OFFLINE"},
		{"未就绪 WireGuard", func(t *testing.T, f *fixture) {
			f.exec(t, `UPDATE node_heartbeats SET ready_protocols='["TROJAN"]'::jsonb WHERE node_id=101`)
		}, "PROTOCOL_UNSUPPORTED"},
		{"没有启用的入站", func(t *testing.T, f *fixture) {
			f.exec(t, `DELETE FROM node_inbounds WHERE node_id=101`)
		}, "PROTOCOL_UNSUPPORTED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			quota := int64(1 << 30)
			f.plan(t, 2, &quota, 0)
			f.node(t, 101, 1, wgParamsJSON())
			f.node(t, 102, 2, wgParamsJSON())
			c.break101(t, f)

			out, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101", "102"))
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Profiles) != 1 || out.Profiles[0].NodeID != "102" {
				t.Fatalf("健康节点应照常下发：%+v", out.Profiles)
			}
			if len(out.Unavailable) != 1 || out.Unavailable[0].NodeID != "101" || out.Unavailable[0].Reason != c.reason {
				t.Fatalf("不可用原因不符，期望 %s，实际 %+v", c.reason, out.Unavailable)
			}
			// 不可用节点绝不携带任何材料。
			raw, _ := json.Marshal(out.Unavailable)
			if strings.Contains(string(raw), wgNodeKey) || strings.Contains(string(raw), "10.20.0.2") {
				t.Fatalf("不可用节点泄露了材料：%s", raw)
			}
		})
	}
}

// 请求的节点全部不可用时，整体按 50302 拒绝，而不是退化成返回空列表。
func TestConnectProfilesRefusesWhenAllNodesUnavailableIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())
	f.exec(t, `UPDATE nodes SET status='MAINTENANCE' WHERE id=101`)

	_, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrNodeUnavailable)
}

func TestConnectProfilesSkipsDeviceWithoutKeyIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())
	// 设备尚未上传公钥：没有材料可下发，但不是错误，按协议不支持处理。
	f.exec(t, `UPDATE device_credentials SET wg_pubkey=NULL,wg_address=NULL WHERE device_id=$1`, f.device)

	_, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	code(t, err, httpx.ErrNodeUnavailable)
}

func TestConnectProfilesMixesAvailableAndUnavailableIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())
	f.node(t, 102, 1, wgParamsJSON())
	f.exec(t, `UPDATE nodes SET status='MAINTENANCE' WHERE id=102`)

	out, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101", "102", "103"))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Profiles) != 1 || out.Profiles[0].NodeID != "101" {
		t.Fatalf("可用节点不符：%+v", out.Profiles)
	}
	if len(out.Unavailable) != 2 {
		t.Fatalf("不可用节点数不符：%+v", out.Unavailable)
	}
	// 不存在的节点不得泄露「它不存在」，与维护中同样处理。
	reasons := map[string]string{}
	for _, u := range out.Unavailable {
		reasons[u.NodeID] = u.Reason
	}
	if reasons["102"] != "MAINTENANCE" || reasons["103"] != "MAINTENANCE" {
		t.Fatalf("不可用原因不符：%v", reasons)
	}
}

func TestConnectProfilesRejectsBadInputIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	f.node(t, 101, 1, wgParamsJSON())

	cases := []Input{
		{NodeIDs: nil, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"101", "102", "103", "104", "105", "106"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"101", "101"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"0"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"-1"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"01"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"abc"}, Protocols: []string{"WIREGUARD"}},
		{NodeIDs: []string{"101"}, Protocols: nil},
		{NodeIDs: []string{"101"}, Protocols: []string{"SOCKS5"}},
		{NodeIDs: []string{"101"}, Protocols: []string{"WIREGUARD", "WIREGUARD"}},
	}
	for i, in := range cases {
		if _, err := f.svc.Profiles(f.ctx, f.userID, f.device, in); err == nil {
			t.Fatalf("第 %d 组非法输入被接受：%+v", i, in)
		} else {
			code(t, err, httpx.ErrBadRequest)
		}
	}
}

func TestConnectProfilesExpiryBoundedByEntitlementIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	// 套餐 1 分钟后到期：材料有效期不得超过它。
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,device_limit,expire_at,period_quota_bytes,period_used_bytes)
 VALUES($1,1,3,$2,$3,0)`, f.userID, f.clock.now.Add(time.Minute), quota)
	f.node(t, 101, 1, wgParamsJSON())

	out, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101"))
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if expires.After(f.clock.now.Add(time.Minute)) {
		t.Fatalf("材料有效期超过了权益到期时间：%s", out.ExpiresAt)
	}
}

func TestConnectProfilesRejectsBrokenNodeParamsIntegration(t *testing.T) {
	f := newFixture(t)
	quota := int64(1 << 30)
	f.plan(t, 1, &quota, 0)
	for _, params := range []string{
		`{"interface":"wg0","public_key":"not-a-key","config_version":"7"}`,
		fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"7","mtu":100}`, wgNodeKey),
		fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"7","dns":["not-an-ip"]}`, wgNodeKey),
		fmt.Sprintf(`{"interface":"wg0","public_key":%q,"config_version":"7","extra":1}`, wgNodeKey),
	} {
		f := newFixture(t)
		f.plan(t, 1, &quota, 0)
		f.node(t, 101, 1, params)
		if _, err := f.svc.Profiles(f.ctx, f.userID, f.device, request("101")); err == nil {
			t.Fatalf("非法入站参数被接受：%s", params)
		}
	}
}
