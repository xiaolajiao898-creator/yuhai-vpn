package nodecontrol

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/nodeagent"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 流量链路的闭环证明：节点侧按凭据采样 →（真实 mTLS）→ 控制面入库 → 扣减权益。
//
// 与 WireGuard 全链路测试同样的边界：内核由替身扮演（按 wg show transfer 的真实格式
// 回答累计计数器），其余环节——租约映射、持久基线、mTLS 传输、幂等入账、权益扣减——
// 都是真实代码。**不能据此声称真实隧道的流量计费已验收**。

// transferKernel 扮演一个按公钥累计收发字节的接口。
type transferKernel struct {
	iface   string
	counter map[string][2]int64 // 公钥 → {rx, tx}
}

func (k transferKernel) Show(_ context.Context, iface, field string) ([]byte, error) {
	if iface != k.iface || field != "transfer" {
		return nil, errors.New("unexpected query")
	}
	var b strings.Builder
	for key, v := range k.counter {
		b.WriteString(fmt.Sprintf("%s\t%d\t%d\n", key, v[0], v[1]))
	}
	return []byte(b.String()), nil
}

func TestTrafficAgentToBillingChainIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	quota := int64(10000)
	subscription(t, f, 11, &quota, 0, f.clock.Now().Add(time.Hour))

	ts := chainServer(t, f)
	client, dir := chainClient(t, f, ts.URL)

	// 1. 取清单并落盘为租约文件——采样要靠它把公钥映射回凭据。
	lease, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	syncDir := filepath.Join(dir, "sync")
	if err := nodeagent.InitWGSyncStore(syncDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	sync, err := nodeagent.OpenWGSyncStore(syncDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sync.Close()
	if err := sync.Publish(lease, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if len(lease.Manifest.Peers) != 1 {
		t.Fatalf("清单应当只有一个 peer：%v", lease.Manifest.Peers)
	}
	peer := lease.Manifest.Peers[0].PublicKey

	// 2. 组装节点侧上报器。内核计数器：rx=600（用户上行），tx=400（用户下行）。
	kernel := transferKernel{iface: lease.Manifest.Interface, counter: map[string][2]int64{peer: {600, 400}}}
	trafficDir := filepath.Join(dir, "traffic")
	if err := nodeagent.InitTrafficStore(trafficDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenTrafficStore(trafficDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := &nodeagent.TrafficRunner{
		Store: store,
		Sampler: &nodeagent.WGTrafficSampler{
			Clock: f.clock, NodeID: client.NodeID(), ManifestFile: sync.ManifestPath(),
			Reader: kernel, InterfaceByName: upLink,
		},
		Sender: client,
		Clock:  f.clock,
	}

	// 3. 第一次上报：全量 600/400 经真实 mTLS 送达并落账。
	result, err := runner.Step(f.ctx)
	if err != nil {
		t.Fatalf("首次上报失败：%v", err)
	}
	if result.State != "sent" || result.ItemCount != 1 || result.Duplicate {
		t.Fatalf("首次上报结果不符：%+v", result)
	}
	if up, down := dailyTotals(t, f, 11); up != 600 || down != 400 {
		t.Fatalf("方向或金额入账错误：up=%d down=%d（rx 应当记为上行）", up, down)
	}
	if used := subUsed(t, f, 11); used != 1000 {
		t.Fatalf("权益扣减不符，实际 %d", used)
	}

	// 4. 计数器未变：不应再发，也不应再扣。
	if result, err = runner.Step(f.ctx); err != nil || result.State != "idle" {
		t.Fatalf("无增量时不应上报：%+v %v", result, err)
	}
	if used := subUsed(t, f, 11); used != 1000 {
		t.Fatalf("无增量却发生了扣减，实际 %d", used)
	}

	// 5. 计数器增长后只上报增量。
	f.clock.Add(time.Minute)
	if err := sync.Publish(mustFetch(t, client, f), f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	kernel.counter[peer] = [2]int64{900, 400}
	if result, err = runner.Step(f.ctx); err != nil || result.State != "sent" {
		t.Fatalf("增量上报失败：%+v %v", result, err)
	}
	if up, down := dailyTotals(t, f, 11); up != 900 || down != 400 {
		t.Fatalf("增量累加错误：up=%d down=%d", up, down)
	}
	if used := subUsed(t, f, 11); used != 1300 {
		t.Fatalf("增量扣减错误，实际 %d", used)
	}
}

func mustFetch(t *testing.T, client *nodeagent.Client, f *heartbeatFixture) nodewire.WGLease {
	t.Helper()
	l, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestTrafficChainSurvivesLostAcknowledgementIntegration 证明链路两端对「响应丢失」
// 的处理是一致的：节点重发同一个 report_id，控制面按幂等键去重，用户不会被多扣。
func TestTrafficChainSurvivesLostAcknowledgementIntegration(t *testing.T) {
	f := newTrafficFixture(t)
	addWGUser(t, f, 11)
	quota := int64(10000)
	subscription(t, f, 11, &quota, 0, f.clock.Now().Add(time.Hour))

	ts := chainServer(t, f)
	client, dir := chainClient(t, f, ts.URL)
	lease, err := client.FetchWireGuard(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	syncDir := filepath.Join(dir, "sync")
	if err := nodeagent.InitWGSyncStore(syncDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	sync, err := nodeagent.OpenWGSyncStore(syncDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer sync.Close()
	if err := sync.Publish(lease, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	peer := lease.Manifest.Peers[0].PublicKey

	trafficDir := filepath.Join(dir, "traffic")
	if err := nodeagent.InitTrafficStore(trafficDir, client.NodeID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	store, err := nodeagent.OpenTrafficStore(trafficDir, client.NodeID(), ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	kernel := transferKernel{iface: lease.Manifest.Interface, counter: map[string][2]int64{peer: {500, 0}}}
	sampler := &nodeagent.WGTrafficSampler{
		Clock: f.clock, NodeID: client.NodeID(), ManifestFile: sync.ManifestPath(),
		Reader: kernel, InterfaceByName: upLink,
	}

	// 第一次：控制面已入账，但节点没收到回执（用一个必定失败的 sender 模拟）。
	lost := &lostAckSender{inner: client}
	runner := &nodeagent.TrafficRunner{Store: store, Sampler: sampler, Sender: lost, Clock: f.clock}
	if _, err := runner.Step(f.ctx); err == nil {
		t.Fatal("回执丢失却报告成功")
	}
	if used := subUsed(t, f, 11); used != 500 {
		t.Fatalf("控制面应当已入账 500，实际 %d", used)
	}
	pending := store.Pending()
	if pending == nil {
		t.Fatal("回执丢失后未保留待确认上报")
	}

	// 第二次：原样重发同一个 report_id。控制面识别为重复，绝不重复扣减。
	runner.Sender = client
	result, err := runner.Step(f.ctx)
	if err != nil || result.State != "resent" || !result.Duplicate {
		t.Fatalf("重发未被识别为重复：%+v %v", result, err)
	}
	if used := subUsed(t, f, 11); used != 500 {
		t.Fatalf("重发导致重复计费，实际 %d", used)
	}
	if store.Pending() != nil {
		t.Fatal("确认后未清除待确认上报")
	}
}

// lostAckSender 真的把请求发出去（控制面会入账），但对调用方报告失败。
type lostAckSender struct{ inner *nodeagent.Client }

func (s *lostAckSender) ReportTraffic(ctx context.Context, p nodeagent.TrafficPending) (nodewire.TrafficReceipt, error) {
	// 请求真的发出去了，控制面会入账；只是调用方拿不到回执。
	if _, err := s.inner.ReportTraffic(ctx, p); err != nil {
		return nodewire.TrafficReceipt{}, err
	}
	return nodewire.TrafficReceipt{}, errors.New("回执在网络中丢失")
}
