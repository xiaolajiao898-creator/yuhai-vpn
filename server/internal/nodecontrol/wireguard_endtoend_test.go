package nodecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/connect"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 服务端侧「设备拿到一份能用的 WireGuard 配置」由三段代码组成，各自有自己的测试：
//
//	PUT /v1/devices/current/wg-key   → 写 device_credentials.wg_pubkey / wg_address
//	POST /v1/connect/profiles        → 把材料发给**设备**
//	WireGuardManifest                → 把 peer 清单发给**节点**
//
// 各自绿不代表连得上：只要后两段对同一台设备的理解有任何一处不一致——公钥不同、
// 地址不同、一边认为可用另一边认为不可用——隧道就是静默不通，而且三个包的测试全是绿的。
// 这个测试就是钉住那个接缝：客户端拿到的材料，必须与节点被告知的 peer 完全对得上。
func TestWireGuardClientMaterialMatchesNodeManifestIntegration(t *testing.T) {
	f := newWGFixture(t)
	role := pgx.Identifier{f.pool.Config().ConnConfig.User}.Sanitize()
	// 上传公钥要写这三列；其余列仍然只读，越权会当场失败而不是悄悄通过。
	f.exec(t, "GRANT UPDATE(wg_pubkey,wg_address,rotated_at) ON device_credentials TO "+role)
	f.exec(t, "GRANT SELECT(id,status) ON devices TO "+role)

	const userID = 4242
	lookup := sha256.Sum256([]byte("endtoend"))
	f.exec(t, `INSERT INTO users(id,account_no,secret_lookup,invite_code) OVERRIDING SYSTEM VALUE
 VALUES($1,$2,$3,$4)`, int64(userID), fmt.Sprintf("%010d", userID), hex.EncodeToString(lookup[:]), "E2E")
	f.exec(t, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform) OVERRIDING SYSTEM VALUE
 VALUES($1,$1,$2,'device-signing-key','ANDROID')`, int64(userID), uuid.NewString())
	// 关键：这台设备**还没有**上传 WireGuard 公钥，所以两列都是空的。
	f.exec(t, `INSERT INTO device_credentials(id,device_id,proxy_uuid,proxy_secret_enc) OVERRIDING SYSTEM VALUE
 VALUES($1,$1,$2,$3)`, int64(userID), uuid.NewString(), []byte("secret-not-selected"))
	f.exec(t, `INSERT INTO subscriptions(user_id,tier,device_limit,expire_at,period_quota_bytes)
 VALUES($1,1,3,$2,1000000)`, int64(userID), f.clock.Now().Add(time.Hour))

	// 节点上报一次心跳，声明 WIREGUARD 就绪——App 侧与连接材料共用这道门槛。
	h := heartbeatAt(f.clock, "1")
	h.ConfigVersion = "1"
	h.SnapshotChecksum = strings.Repeat("a", 64)
	h.ReadyProtocols = []string{"WIREGUARD"}
	if _, err := f.accept(h); err != nil {
		t.Fatal(err)
	}

	nodeID := "9007199254740993"
	profiles := &connect.Service{Pool: f.pool, Clock: f.clock}
	input := connect.Input{NodeIDs: []string{nodeID}, Protocols: []string{"WIREGUARD"}}

	// 上传公钥之前：没有材料可发，且节点清单里也不该有这台设备。
	if _, err := profiles.Profiles(f.ctx, userID, userID, input); err == nil {
		t.Fatal("没有公钥也发出了连接材料")
	} else {
		expectCode(t, err, httpx.ErrNodeUnavailable)
	}
	for _, p := range getWG(t, f).Manifest.Peers {
		if p.CredentialID == strconv.Itoa(userID) {
			t.Fatal("未上传公钥的设备出现在节点清单里")
		}
	}

	// 上传公钥：这一步同时分配隧道地址。
	_, pool, err := net.ParseCIDR("10.77.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	accounts := &account.Service{Repo: &account.Repo{Pool: f.pool}, Clock: f.clock, WGPool: pool, WGReserved: 16}
	deviceKey := wgTestKey(200)
	if err := accounts.SetWireGuardKey(f.ctx, userID, account.WGKeyInput{PubKey: deviceKey}); err != nil {
		t.Fatal(err)
	}

	// 设备侧拿到的材料。
	out, err := profiles.Profiles(f.ctx, userID, userID, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Profiles) != 1 || len(out.Profiles[0].Outbounds) != 1 {
		t.Fatalf("连接材料不符：%+v", out)
	}
	client := out.Profiles[0].Outbounds[0]

	// 节点侧拿到的 peer。
	var peerKey, peerIP string
	for _, p := range getWG(t, f).Manifest.Peers {
		if p.CredentialID == strconv.Itoa(userID) {
			peerKey = p.PublicKey
			if len(p.AllowedIPs) != 1 {
				t.Fatalf("peer 的 AllowedIPs 应当只有自己的 /32：%v", p.AllowedIPs)
			}
			peerIP = p.AllowedIPs[0]
		}
	}
	if peerKey == "" {
		t.Fatal("上传公钥后节点清单里仍然没有这台设备")
	}

	// 接缝本身：两边必须说的是同一台设备。
	if peerKey != deviceKey {
		t.Fatalf("节点看到的公钥与设备上传的不一致：%s ≠ %s", peerKey, deviceKey)
	}
	if peerIP != client.Address {
		t.Fatalf("节点为该 peer 放行的地址与下发给设备的隧道地址不一致：%s ≠ %s", peerIP, client.Address)
	}
	// 客户端材料里的服务端公钥必须来自节点入站配置，而不是设备自己的公钥。
	if client.ServerPublicKey == deviceKey || client.ServerPublicKey != wgTestKey(1) {
		t.Fatalf("下发的服务端公钥有误：%s", client.ServerPublicKey)
	}
	if client.Endpoint != "private-host.invalid:51820" {
		t.Fatalf("端点不符：%s", client.Endpoint)
	}

	// 轮换公钥后两边必须同步改变，不能一边新一边旧。
	rotated := wgTestKey(201)
	if err := accounts.SetWireGuardKey(f.ctx, userID, account.WGKeyInput{PubKey: rotated}); err != nil {
		t.Fatal(err)
	}
	out, err = profiles.Profiles(f.ctx, userID, userID, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Profiles[0].Outbounds[0].Address != client.Address {
		t.Fatal("轮换公钥不应更换隧道地址")
	}
	var after string
	for _, p := range getWG(t, f).Manifest.Peers {
		if p.CredentialID == strconv.Itoa(userID) {
			after = p.PublicKey
		}
	}
	if after != rotated {
		t.Fatalf("轮换后节点清单仍是旧公钥：%s", after)
	}

	// 设备被移除后，节点清单里立刻没有它——这是撤销真正生效的前提。
	f.exec(t, `UPDATE device_credentials SET status='REVOKED' WHERE id=$1`, int64(userID))
	for _, p := range getWG(t, f).Manifest.Peers {
		if p.CredentialID == strconv.Itoa(userID) {
			t.Fatal("凭据已撤销但仍在节点清单里")
		}
	}
}
