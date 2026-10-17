package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 这一组测试盯的是计费不出错的三件事：方向不能反、增量不能算错、
// 未确认的上报不能丢也不能重复计费。

const trafficOrigin = "https://node.invalid"

// transferReader 按 wg show <iface> transfer 的真实格式回答：<公钥>\t<rx>\t<tx>。
type transferReader struct {
	iface string
	rows  string
	err   error
}

func (r transferReader) Show(_ context.Context, iface, field string) ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	if iface != r.iface || field != "transfer" {
		return nil, errors.New("unexpected query")
	}
	return []byte(r.rows), nil
}

func trafficFixture(t *testing.T, rows string) (*WGTrafficSampler, WGManifest, *fixedClock) {
	t.Helper()
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	put(t, path, leaseBytes(t, m, clk.now))
	return &WGTrafficSampler{
		Clock: clk, NodeID: m.NodeID, ManifestFile: path,
		Reader:          transferReader{iface: m.Interface, rows: rows},
		InterfaceByName: upLinkForApply,
	}, m, clk
}

func TestTrafficSampleMapsDirectionAndCredentials(t *testing.T) {
	m := wgManifest()
	// rx=用户上行，tx=用户下行。两列刻意不同，方向反了立刻看得出来。
	rows := fmt.Sprintf("%s\t100\t900\n%s\t7\t8\n", m.Peers[0].PublicKey, m.Peers[1].PublicKey)
	s, _, _ := trafficFixture(t, rows)

	counters, credentials, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 2 {
		t.Fatalf("采样条目数不符：%v", counters)
	}
	if credentials[m.Peers[0].PublicKey] != m.Peers[0].CredentialID {
		t.Fatal("公钥未正确映射回凭据")
	}
	items, err := Delta(nil, counters, credentials)
	if err != nil {
		t.Fatal(err)
	}
	byCredential := map[string]nodewire.TrafficItem{}
	for _, i := range items {
		byCredential[i.CredentialID] = i
	}
	first := byCredential[m.Peers[0].CredentialID]
	if first.UpBytes != "100" || first.DownBytes != "900" {
		t.Fatalf("方向映射错误：rx 应当是上行、tx 是下行，实际 up=%s down=%s", first.UpBytes, first.DownBytes)
	}
}

func TestTrafficSampleIgnoresPeersOutsideManifest(t *testing.T) {
	m := wgManifest()
	// 第三个 peer 不在清单里：无法归属，不得替任何人记账。
	rows := fmt.Sprintf("%s\t10\t10\n%s\t0\t0\n%s\t999999\t999999\n", m.Peers[0].PublicKey, m.Peers[1].PublicKey, wgKey(200))
	s, _, _ := trafficFixture(t, rows)

	counters, credentials, err := s.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range counters {
		if c.PublicKey == wgKey(200) {
			t.Fatal("清单外的 peer 被计入采样")
		}
	}
	items, err := Delta(nil, counters, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].UpBytes != "10" {
		t.Fatalf("只应上报有流量且在清单内的 peer：%v", items)
	}
}

func TestTrafficSampleRejectsBadInput(t *testing.T) {
	m := wgManifest()
	cases := []struct {
		name string
		rows string
	}{
		{"字段数不足", m.Peers[0].PublicKey + "\t10\n"},
		{"字段数过多", m.Peers[0].PublicKey + "\t10\t10\t10\n"},
		{"公钥非法", "not-a-key\t10\t10\n"},
		{"计数为负", m.Peers[0].PublicKey + "\t-1\t10\n"},
		{"计数前导零", m.Peers[0].PublicKey + "\t010\t10\n"},
		{"计数超出安全整数", fmt.Sprintf("%s\t%d\t10\n", m.Peers[0].PublicKey, nodewire.MaxTrafficBytes+1)},
		{"同一公钥重复", m.Peers[0].PublicKey + "\t1\t1\n" + m.Peers[0].PublicKey + "\t1\t1\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := trafficFixture(t, c.rows)
			if _, _, err := s.Sample(context.Background()); err == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestTrafficSampleRequiresFreshLeaseAndLiveInterface(t *testing.T) {
	m := wgManifest()
	rows := fmt.Sprintf("%s\t1\t1\n", m.Peers[0].PublicKey)

	// 租约过期：清单不再是当前授权，不能据此归属流量。
	s, _, clk := trafficFixture(t, rows)
	clk.now = clk.now.Add(31 * time.Second)
	if _, _, err := s.Sample(context.Background()); err == nil {
		t.Fatal("过期租约仍被用于采样")
	}

	// 接口不存在或已关闭。
	for _, link := range []func(string) (*net.Interface, error){
		func(string) (*net.Interface, error) { return nil, errors.New("no such interface") },
		func(name string) (*net.Interface, error) { return downLink(3, name), nil },
	} {
		s, _, _ := trafficFixture(t, rows)
		s.InterfaceByName = link
		if _, _, err := s.Sample(context.Background()); err == nil {
			t.Fatal("接口不可用时仍然采样")
		}
	}

	// 配置的接口名与租约不一致：说明配置错了，不能继续。
	s, _, _ = trafficFixture(t, rows)
	s.Interface = "wg9"
	if _, _, err := s.Sample(context.Background()); err == nil {
		t.Fatal("接口名不一致仍然采样")
	}
}

func TestTrafficDeltaHandlesCounterReset(t *testing.T) {
	m := wgManifest()
	credentials := map[string]string{m.Peers[0].PublicKey: m.Peers[0].CredentialID}
	baseline := []TrafficCounter{{PublicKey: m.Peers[0].PublicKey, RX: "1000", TX: "2000"}}

	// 正常增长：只上报差值。
	items, err := Delta(baseline, []TrafficCounter{{PublicKey: m.Peers[0].PublicKey, RX: "1500", TX: "2500"}}, credentials)
	if err != nil || len(items) != 1 || items[0].UpBytes != "500" || items[0].DownBytes != "500" {
		t.Fatalf("增量计算错误：%v %v", items, err)
	}

	// 计数器回退＝peer 重建或接口重启：当前值本身就是重置后的全部流量。
	items, err = Delta(baseline, []TrafficCounter{{PublicKey: m.Peers[0].PublicKey, RX: "30", TX: "40"}}, credentials)
	if err != nil || len(items) != 1 || items[0].UpBytes != "30" || items[0].DownBytes != "40" {
		t.Fatalf("重置后应当上报当前值：%v %v", items, err)
	}

	// 单方向回退也按各自方向处理，不得把另一方向一起重置。
	items, err = Delta(baseline, []TrafficCounter{{PublicKey: m.Peers[0].PublicKey, RX: "30", TX: "2500"}}, credentials)
	if err != nil || items[0].UpBytes != "30" || items[0].DownBytes != "500" {
		t.Fatalf("单方向重置处理错误：%v %v", items, err)
	}

	// 没有变化的 peer 不占用上报条目。
	items, err = Delta(baseline, []TrafficCounter{{PublicKey: m.Peers[0].PublicKey, RX: "1000", TX: "2000"}}, credentials)
	if err != nil || len(items) != 0 {
		t.Fatalf("无变化不应产生条目：%v %v", items, err)
	}
}

// ------------------------------------------------------------------ 状态存储

func newTrafficStore(t *testing.T) (*TrafficStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "traffic")
	if err := InitTrafficStore(dir, "9007199254740993", trafficOrigin); err != nil {
		t.Fatal(err)
	}
	s, err := OpenTrafficStore(dir, "9007199254740993", trafficOrigin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir
}

func samplePending(id string) TrafficPending {
	return TrafficPending{
		ReportID:    id,
		WindowStart: "2026-09-13T09:59:00Z",
		WindowEnd:   "2026-09-13T10:00:00Z",
		Items:       []nodewire.TrafficItem{{CredentialID: "7", UpBytes: "10", DownBytes: "20"}},
		Snapshot:    []TrafficCounter{{PublicKey: wgKey(2), RX: "10", TX: "20"}},
	}
}

const trafficReportID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

func TestTrafficStoreIdentityLockAndPermissions(t *testing.T) {
	s, dir := newTrafficStore(t)
	if got := perm(t, dir); got != 0700 {
		t.Fatalf("状态目录权限应为 0700，实际 %o", got)
	}
	if got := perm(t, filepath.Join(dir, "traffic.json")); got != 0600 {
		t.Fatalf("状态文件权限应为 0600，实际 %o", got)
	}
	// 同一目录不得被两个进程同时持有。
	if second, err := OpenTrafficStore(dir, "9007199254740993", trafficOrigin); err == nil {
		second.Close()
		t.Fatal("状态目录被两个持有者同时打开")
	}
	// 身份不匹配一律拒绝。
	s.Close()
	for _, bad := range [][2]string{{"7", trafficOrigin}, {"9007199254740993", "https://other.invalid"}} {
		if other, err := OpenTrafficStore(dir, bad[0], bad[1]); err == nil {
			other.Close()
			t.Fatalf("身份不匹配仍被打开：%v", bad)
		}
	}
}

func TestTrafficStoreKeepsPendingUntilCommitted(t *testing.T) {
	s, dir := newTrafficStore(t)
	p := samplePending(trafficReportID)
	if err := s.Stage(p); err != nil {
		t.Fatal(err)
	}
	// 未确认前基线不得前移——否则这段流量永久丢失。
	if len(s.Baseline()) != 0 {
		t.Fatal("上报未确认基线就前移了")
	}
	// 也不得被新的上报顶掉。
	if err := s.Stage(samplePending("3f2504e0-4f89-41d3-9a0c-0305e82c3302")); err == nil {
		t.Fatal("待确认上报被新上报覆盖")
	}

	// 重启后必须原样恢复同一个 report_id，交给服务端按幂等键去重。
	s.Close()
	reopened, err := OpenTrafficStore(dir, "9007199254740993", trafficOrigin)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pending := reopened.Pending()
	if pending == nil || pending.ReportID != trafficReportID || len(pending.Items) != 1 {
		t.Fatalf("重启后未恢复待确认上报：%+v", pending)
	}
	if err := reopened.Commit(); err != nil {
		t.Fatal(err)
	}
	if reopened.Pending() != nil {
		t.Fatal("确认后仍保留待确认上报")
	}
	base := reopened.Baseline()
	if len(base) != 1 || base[0].RX != "10" || base[0].TX != "20" {
		t.Fatalf("确认后基线未前移到快照：%v", base)
	}
	if reopened.LastWindowEnd() != p.WindowEnd {
		t.Fatalf("窗口结束时间未推进：%q", reopened.LastWindowEnd())
	}
}

func TestTrafficStoreRejectsInvalidStageAndUseAfterClose(t *testing.T) {
	s, _ := newTrafficStore(t)
	bad := samplePending("not-a-uuid")
	if err := s.Stage(bad); err == nil {
		t.Fatal("非法 report_id 被落盘")
	}
	s.Close()
	if err := s.Stage(samplePending(trafficReportID)); err == nil {
		t.Fatal("关闭后仍允许落盘")
	}
	if err := s.Commit(); err == nil {
		t.Fatal("关闭后仍允许确认")
	}
}

// ------------------------------------------------------------------ 串联

type recordingSender struct {
	calls []TrafficPending
	err   error
	dup   bool
}

func (s *recordingSender) ReportTraffic(_ context.Context, p TrafficPending) (nodewire.TrafficReceipt, error) {
	s.calls = append(s.calls, p)
	if s.err != nil {
		return nodewire.TrafficReceipt{}, s.err
	}
	return nodewire.TrafficReceipt{Accepted: true, Duplicate: s.dup}, nil
}

func trafficRunner(t *testing.T, rows string) (*TrafficRunner, *recordingSender, *TrafficStore) {
	t.Helper()
	sampler, m, clk := trafficFixture(t, rows)
	store, _ := newTrafficStore(t)
	sampler.NodeID = m.NodeID
	sender := &recordingSender{}
	return &TrafficRunner{Store: store, Sampler: sampler, Sender: sender, Clock: clk}, sender, store
}

func TestTrafficRunnerAdvancesOnlyAfterAcknowledgement(t *testing.T) {
	m := wgManifest()
	rows := fmt.Sprintf("%s\t100\t200\n", m.Peers[0].PublicKey)
	r, sender, store := trafficRunner(t, rows)

	// 第一次发送失败：pending 必须保留，基线不得前移。
	sender.err = errors.New("控制面不可达")
	if _, err := r.Step(context.Background()); err == nil {
		t.Fatal("发送失败却报告成功")
	}
	pending := store.Pending()
	if pending == nil || len(store.Baseline()) != 0 {
		t.Fatal("发送失败后丢失了待确认上报或提前前移了基线")
	}
	firstID := pending.ReportID

	// 第二轮：必须重发同一个 report_id，而不是采新样本另起一份。
	sender.err = nil
	result, err := r.Step(context.Background())
	if err != nil || result.State != "resent" {
		t.Fatalf("未重发待确认上报：%+v %v", result, err)
	}
	if len(sender.calls) != 2 || sender.calls[1].ReportID != firstID {
		t.Fatalf("重发时换了 report_id，会导致重复计费：%v", sender.calls)
	}
	if store.Pending() != nil {
		t.Fatal("确认后未清除待确认上报")
	}
	base := store.Baseline()
	if len(base) != 1 || base[0].RX != "100" || base[0].TX != "200" {
		t.Fatalf("确认后基线未前移：%v", base)
	}

	// 第三轮：计数器未变化，无流量可报。
	result, err = r.Step(context.Background())
	if err != nil || result.State != "idle" || len(sender.calls) != 2 {
		t.Fatalf("无流量时不应发送：%+v %v", result, err)
	}
}

func TestTrafficRunnerReportsIncrementOnly(t *testing.T) {
	m := wgManifest()
	r, sender, store := trafficRunner(t, fmt.Sprintf("%s\t100\t200\n", m.Peers[0].PublicKey))
	if _, err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sender.calls[0].Items[0].UpBytes != "100" {
		t.Fatalf("首次应上报全量：%v", sender.calls[0].Items)
	}
	// 时间推进到下一个上报窗口，计数器继续增长：只上报增量。
	clk := r.Clock.(*fixedClock)
	clk.now = clk.now.Add(time.Minute)
	r.Sampler.Clock = clk
	// 租约也要保持新鲜，否则采样会因过期而拒绝。
	put(t, r.Sampler.ManifestFile, leaseBytes(t, m, clk.now))
	r.Sampler.Reader = transferReader{iface: m.Interface, rows: fmt.Sprintf("%s\t250\t200\n", m.Peers[0].PublicKey)}
	if _, err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := sender.calls[len(sender.calls)-1]
	if last.Items[0].UpBytes != "150" || last.Items[0].DownBytes != "0" {
		t.Fatalf("第二次应只上报增量 150/0，实际 %v", last.Items)
	}
	if last.ReportID == sender.calls[0].ReportID {
		t.Fatal("不同窗口复用了同一个 report_id")
	}
	if last.WindowStart != sender.calls[0].WindowEnd {
		t.Fatalf("窗口未首尾相接：%s vs %s", last.WindowStart, sender.calls[0].WindowEnd)
	}
	if base := store.Baseline(); base[0].RX != "250" {
		t.Fatalf("基线未更新到最新快照：%v", base)
	}
}

func TestTrafficRunnerRejectsIncompleteConfiguration(t *testing.T) {
	r, _, _ := trafficRunner(t, "")
	for _, mutate := range []func(*TrafficRunner){
		func(r *TrafficRunner) { r.Store = nil },
		func(r *TrafficRunner) { r.Sampler = nil },
		func(r *TrafficRunner) { r.Sender = nil },
		func(r *TrafficRunner) { r.Clock = nil },
	} {
		copied := *r
		mutate(&copied)
		if _, err := copied.Step(context.Background()); err == nil {
			t.Fatal("配置不完整仍然执行")
		}
	}
}

func TestTrafficStoreInitRejectsInvalidIdentity(t *testing.T) {
	for _, c := range [][3]string{
		{"relative/dir", "7", trafficOrigin},
		{filepath.Join(t.TempDir(), "a"), "0", trafficOrigin},
		{filepath.Join(t.TempDir(), "b"), "007", trafficOrigin},
		{filepath.Join(t.TempDir(), "c"), "7", "http://node.invalid"},
		{filepath.Join(t.TempDir(), "d"), "7", "https://node.invalid/x"},
	} {
		if err := InitTrafficStore(c[0], c[1], c[2]); err == nil {
			t.Fatalf("非法身份被接受：%v", c)
		}
	}
	// 已存在的目录不得重复初始化，避免覆盖别的节点状态。
	dir := filepath.Join(t.TempDir(), "traffic")
	if err := InitTrafficStore(dir, "7", trafficOrigin); err != nil {
		t.Fatal(err)
	}
	if err := InitTrafficStore(dir, "7", trafficOrigin); err == nil {
		t.Fatal("重复初始化被接受")
	}
	if _, err := os.Stat(filepath.Join(dir, "traffic.json")); err != nil {
		t.Fatal(err)
	}
}
