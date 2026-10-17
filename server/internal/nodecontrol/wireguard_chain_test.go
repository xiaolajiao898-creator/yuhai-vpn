package nodecontrol

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/nodeagent"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 第 18 轮留下的最后一环：控制面清单 → node-sync 取件 → 租约文件 → observer → 心跳。
//
// 本文件把这条链路端到端接起来，真实的部分是：TLS 1.3 双向认证的 HTTP 传输、控制面按
// 身份筛选出的公开清单、客户端的 nonce 绑定与租约校验、落盘的原子写与权限、observer 对
// 租约文件的二次读取与时效复核、以及经同一条 mTLS 通道回传并落库的心跳。
//
// 唯一被替身取代的是 Linux 内核本身：fakeKernel 扮演一个"已经按清单配置好"的 WireGuard
// 接口。因此本测试证明的是控制链路正确，**不能据此声称真实隧道已经打通**——真实隧道需要
// 在装有 WireGuard 的 Linux 节点上用真实 wg 命令验收。

// fakeKernel 按清单内容回答 wg show，代表一个已按期望状态配置好的接口。
// 它不实施任何配置，也不代表真实内核。
type fakeKernel struct {
	m         nodewire.WGManifest
	handshake int64
}

func (k fakeKernel) Show(_ context.Context, iface, field string) ([]byte, error) {
	if iface != k.m.Interface {
		return nil, errors.New("unexpected interface")
	}
	var b strings.Builder
	switch field {
	case "public-key":
		return []byte(k.m.PublicKey + "\n"), nil
	case "listen-port":
		return []byte(strconv.Itoa(k.m.ListenPort) + "\n"), nil
	case "allowed-ips":
		for _, p := range k.m.Peers {
			b.WriteString(p.PublicKey + "\t" + strings.Join(p.AllowedIPs, "\t") + "\n")
		}
		return []byte(b.String()), nil
	case "latest-handshakes":
		for _, p := range k.m.Peers {
			b.WriteString(p.PublicKey + "\t" + strconv.FormatInt(k.handshake, 10) + "\n")
		}
		return []byte(b.String()), nil
	}
	return nil, errors.New("unexpected field")
}

func upLink(name string) (*net.Interface, error) {
	return &net.Interface{Index: 3, Name: name, Flags: net.FlagUp | net.FlagPointToPoint}, nil
}

// chainServer 用真实 mTLS 配置起一个测试服务端，处理器是打开了清单开关的 fixture 服务。
func chainServer(t *testing.T, f *heartbeatFixture) *httptest.Server {
	t.Helper()
	cert, key, root := f.ca.serverFiles(t)
	srv, err := NewServer("127.0.0.1:0", cert, key, root, f.pool, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(f.svc.Handler())
	ts.TLS = srv.TLSConfig
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

// chainClient 把 fixture 的节点证书写成私有文件，并按生产路径构造真实的 node-agent 客户端。
func chainClient(t *testing.T, f *heartbeatFixture, origin string) (*nodeagent.Client, string) {
	t.Helper()
	_, _, root := f.ca.serverFiles(t)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key")
	keyBytes, err := x509.MarshalPKCS8PrivateKey(f.cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := nodeagent.NewClient(origin, certFile, keyFile, root, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, dir
}

func TestWGControlPlaneToHeartbeatChainIntegration(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 11)
	addWGUser(t, f, 12)

	ts := chainServer(t, f)
	client, dir := chainClient(t, f, ts.URL)

	// 1. 通过真实 mTLS 向控制面取公开清单租约。
	lease, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatalf("控制面取件失败: %v", err)
	}
	if len(lease.Manifest.Peers) != 2 {
		t.Fatalf("清单 peer 数量不符: %d", len(lease.Manifest.Peers))
	}
	if !nodewire.ValidWGNonce(lease.RequestNonce) {
		t.Fatal("租约未绑定合规 nonce")
	}

	// 2. 落盘为短时租约文件（原子写 + 0600）。
	stateDir := filepath.Join(dir, "sync")
	if err := nodeagent.InitWGSyncStore(stateDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenWGSyncStore(stateDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Publish(lease, f.clock.Now()); err != nil {
		t.Fatalf("租约落盘失败: %v", err)
	}
	manifestFile := filepath.Join(stateDir, "manifest.json")
	st, err := os.Stat(manifestFile)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("租约文件权限不是 0600: %v %v", st, err)
	}

	// 3. 落盘内容里绝不能出现任何密钥材料。
	raw, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-not-selected", "proxy_secret", "proxy_uuid", "private_key"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("租约文件泄露了 %q", forbidden)
		}
	}

	// 4. observer 读取该文件并复核时效，产出运行状态。内核由替身扮演。
	observer := &nodeagent.WGObserver{
		Clock:           f.clock,
		NodeID:          client.NodeID(),
		ManifestFile:    manifestFile,
		Reader:          fakeKernel{m: lease.Manifest, handshake: f.clock.Now().Add(-30 * time.Second).Unix()},
		InterfaceByName: upLink,
	}
	status, err := observer.Observe(f.ctx)
	if err != nil {
		t.Fatalf("observer 复核失败: %v", err)
	}
	if status.SnapshotChecksum != lease.Checksum {
		t.Fatal("运行状态校验和与控制面下发的清单不一致")
	}
	if strings.Join(status.ReadyProtocols, ",") != "WIREGUARD" {
		t.Fatalf("就绪协议不符: %v", status.ReadyProtocols)
	}
	if status.OnlineCount == nil || *status.OnlineCount != 2 {
		t.Fatalf("在线 peer 计数不符: %v", status.OnlineCount)
	}

	// 5. 把运行状态经同一条 mTLS 通道回传为心跳。
	h := heartbeatAt(f.clock, "1")
	h.ConfigVersion = status.ConfigVersion
	h.SnapshotChecksum = status.SnapshotChecksum
	h.ReadyProtocols = status.ReadyProtocols
	body, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost, ts.URL+"/node/v1/heartbeat", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err == nil {
		resp.Body.Close()
	}
	// ts.Client() 不带客户端证书，控制面必须拒绝。
	if err == nil && resp.StatusCode == 200 {
		t.Fatal("无客户端证书的心跳被接受")
	}

	receipt, err := f.svc.Accept(f.ctx, f.ca.state(f.cert), h)
	if err != nil {
		t.Fatalf("心跳被拒绝: %v", err)
	}
	if receipt.Sequence != "1" {
		t.Fatalf("回执序号不符: %s", receipt.Sequence)
	}

	// 6. 落库结果必须与链路首端下发的清单一致。
	var seq int64
	var protocols []string
	var checksum string
	if err := f.owner.QueryRow(f.ctx,
		`SELECT sequence, snapshot_checksum, ARRAY(SELECT jsonb_array_elements_text(ready_protocols)) FROM node_heartbeats`,
	).Scan(&seq, &checksum, &protocols); err != nil {
		t.Fatal(err)
	}
	if seq != 1 || checksum != lease.Checksum || strings.Join(protocols, ",") != "WIREGUARD" {
		t.Fatalf("落库结果与清单不一致: seq=%d checksum=%s protocols=%v", seq, checksum, protocols)
	}
}

func TestWGChainObserverRejectsStaleLeaseFile(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 21)
	ts := chainServer(t, f)
	client, dir := chainClient(t, f, ts.URL)

	lease, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "sync")
	if err := nodeagent.InitWGSyncStore(stateDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenWGSyncStore(stateDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Publish(lease, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	observer := &nodeagent.WGObserver{
		Clock:           f.clock,
		NodeID:          client.NodeID(),
		ManifestFile:    filepath.Join(stateDir, "manifest.json"),
		Reader:          fakeKernel{m: lease.Manifest, handshake: f.clock.Now().Unix()},
		InterfaceByName: upLink,
	}
	if _, err := observer.Observe(f.ctx); err != nil {
		t.Fatalf("新鲜租约应当通过: %v", err)
	}

	// 租约只有 30 秒；过期之后同一个文件必须不再被接受，否则重启或断联后会沿用旧授权。
	f.clock.Add(31 * time.Second)
	if _, err := observer.Observe(f.ctx); err == nil {
		t.Fatal("过期租约文件仍被 observer 接受")
	}

	// 过期后再次取件应当拿到新的租约。
	fresh, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatalf("续期取件失败: %v", err)
	}
	if fresh.RequestNonce == lease.RequestNonce {
		t.Fatal("续期租约复用了旧 nonce")
	}
	if err := store.Publish(fresh, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	observer.Reader = fakeKernel{m: fresh.Manifest, handshake: f.clock.Now().Unix()}
	if _, err := observer.Observe(f.ctx); err != nil {
		t.Fatalf("续期后应当恢复: %v", err)
	}
}

func TestWGChainStopsWhenManifestRouteDisabled(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 31)
	f.svc.WireGuardManifestEnabled = false
	ts := chainServer(t, f)
	client, _ := chainClient(t, f, ts.URL)

	_, err := client.FetchWireGuard(f.ctx)
	var delivery *nodeagent.DeliveryError
	if !errors.As(err, &delivery) || delivery.Kind != "snapshot_rejected" || delivery.Code != 404 {
		t.Fatalf("开关关闭时应当明确拒绝取件，实际 %v", err)
	}
}

func TestWGChainKernelDivergenceIsNotReportedReady(t *testing.T) {
	f := newWGFixture(t)
	addWGUser(t, f, 41)
	addWGUser(t, f, 42)
	ts := chainServer(t, f)
	client, dir := chainClient(t, f, ts.URL)

	lease, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "sync")
	if err := nodeagent.InitWGSyncStore(stateDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenWGSyncStore(stateDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Publish(lease, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	manifestFile := filepath.Join(stateDir, "manifest.json")

	// 内核状态与清单不一致时，绝不能上报 WIREGUARD 就绪。
	drift := lease.Manifest
	drift.ListenPort = lease.Manifest.ListenPort + 1
	missingPeer := lease.Manifest
	missingPeer.Peers = missingPeer.Peers[:1]
	renamed := lease.Manifest
	renamed.Interface = "wg1"

	cases := []struct {
		name   string
		reader nodeagent.WGReader
		link   func(string) (*net.Interface, error)
	}{
		{"监听端口不一致", fakeKernel{m: drift, handshake: f.clock.Now().Unix()}, upLink},
		{"内核少了一个 peer", fakeKernel{m: missingPeer, handshake: f.clock.Now().Unix()}, upLink},
		{"接口名不一致", fakeKernel{m: renamed, handshake: f.clock.Now().Unix()}, upLink},
		{"接口不存在", fakeKernel{m: lease.Manifest, handshake: f.clock.Now().Unix()}, func(string) (*net.Interface, error) {
			return nil, errors.New("no such interface")
		}},
		{"接口已关闭", fakeKernel{m: lease.Manifest, handshake: f.clock.Now().Unix()}, func(name string) (*net.Interface, error) {
			return &net.Interface{Index: 3, Name: name}, nil
		}},
		{"握手时间在未来", fakeKernel{m: lease.Manifest, handshake: f.clock.Now().Add(time.Hour).Unix()}, upLink},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			observer := &nodeagent.WGObserver{
				Clock:           f.clock,
				NodeID:          client.NodeID(),
				ManifestFile:    manifestFile,
				Reader:          c.reader,
				InterfaceByName: c.link,
			}
			if _, err := observer.Observe(f.ctx); err == nil {
				t.Fatal("内核与清单不一致却上报就绪")
			}
		})
	}
}
