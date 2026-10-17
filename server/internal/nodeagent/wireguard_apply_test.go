package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

// peer 下发是本项目第一处会改写内核状态的代码，所以这里的重点不是"能不能下发成功"，
// 而是"什么时候必须拒绝下发"。失败闭合、接口身份、下发后收敛复核各有独立用例。

// fakeWG 同时扮演 WGReader 与 WGWriter，维护一份可变的"内核状态"。
type fakeWG struct {
	iface string
	pub   string
	port  int
	peers map[string][]string

	calls  [][]WGPeerChange
	writes int
	setErr error
	inert  bool   // 为真时 SetPeers 不改变状态，用于验证收敛复核
	onSet  func() // 下发瞬间的副作用，用于模拟并发变化
}

func newFakeWG(m nodewire.WGManifest, peers map[string][]string) *fakeWG {
	if peers == nil {
		peers = map[string][]string{}
	}
	return &fakeWG{iface: m.Interface, pub: m.PublicKey, port: m.ListenPort, peers: peers}
}

func (f *fakeWG) Show(_ context.Context, iface, field string) ([]byte, error) {
	if iface != f.iface {
		return nil, errors.New("unexpected interface")
	}
	switch field {
	case "public-key":
		return []byte(f.pub + "\n"), nil
	case "listen-port":
		return []byte(strconv.Itoa(f.port) + "\n"), nil
	case "allowed-ips":
		keys := make([]string, 0, len(f.peers))
		for k := range f.peers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k + "\t" + strings.Join(f.peers[k], " ") + "\n")
		}
		return []byte(b.String()), nil
	}
	return nil, errors.New("unexpected field")
}

func (f *fakeWG) SetPeers(_ context.Context, iface string, changes []WGPeerChange) error {
	f.writes++
	f.calls = append(f.calls, changes)
	if f.onSet != nil {
		f.onSet()
	}
	if f.setErr != nil {
		return f.setErr
	}
	if iface != f.iface {
		return errors.New("unexpected interface")
	}
	if f.inert {
		return nil
	}
	for _, c := range changes {
		if c.Remove {
			delete(f.peers, c.PublicKey)
			continue
		}
		ips := append([]string(nil), c.AllowedIPs...)
		sort.Strings(ips)
		f.peers[c.PublicKey] = ips
	}
	return nil
}

// applyFixture 写好一份有效的租约文件，并返回其清单与 applier。
func applyFixture(t *testing.T, kernel *fakeWG, clk *fixedClock) (*WGApplier, nodewire.WGManifest) {
	t.Helper()
	m := wgManifest()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	put(t, path, leaseBytes(t, m, clk.now))
	return &WGApplier{
		Clock:           clk,
		NodeID:          m.NodeID,
		ManifestFile:    path,
		Reader:          kernel,
		Writer:          kernel,
		InterfaceByName: upLinkForApply,
	}, m
}

func fakeLink(index int, name string) *net.Interface {
	return &net.Interface{Index: index, Name: name, Flags: net.FlagUp | net.FlagPointToPoint}
}
func downLink(index int, name string) *net.Interface {
	return &net.Interface{Index: index, Name: name}
}
func loopbackLink(index int, name string) *net.Interface {
	return &net.Interface{Index: index, Name: name, Flags: net.FlagUp | net.FlagLoopback}
}
func upLinkForApply(name string) (*net.Interface, error) { return fakeLink(3, name), nil }

func desiredPeers(m nodewire.WGManifest) map[string][]string {
	out := map[string][]string{}
	for _, p := range m.Peers {
		ips := append([]string(nil), p.AllowedIPs...)
		sort.Strings(ips)
		out[p.PublicKey] = ips
	}
	return out
}

func TestWGApplyIsIdempotentWhenKernelAlreadyMatches(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	kernel := newFakeWG(m, desiredPeers(m))
	applier, _ := applyFixture(t, kernel, clk)

	r, err := applier.Apply(context.Background())
	if err != nil {
		t.Fatalf("一致状态下发报错: %v", err)
	}
	if r.Changed() {
		t.Fatalf("内核已一致却报告有变更: %+v", r)
	}
	if kernel.writes != 0 {
		t.Fatal("内核已一致却仍执行了写入")
	}
}

func TestWGApplyAddsUpdatesAndRemoves(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	stale := wgKey(200)
	kernel := newFakeWG(m, map[string][]string{
		// 第一个 peer 的 allowed-ips 与清单不符 → 更新
		m.Peers[0].PublicKey: {"10.20.0.99/32"},
		// 清单里没有的 peer → 删除
		stale: {"10.20.0.250/32"},
		// 第二个 peer 缺失 → 新增
	})
	applier, _ := applyFixture(t, kernel, clk)

	r, err := applier.Apply(context.Background())
	if err != nil {
		t.Fatalf("下发失败: %v", err)
	}
	if r.Added != 1 || r.Updated != 1 || r.Removed != 1 {
		t.Fatalf("差集计算错误: %+v", r)
	}
	if kernel.writes != 1 {
		t.Fatalf("应当只写入一次，实际 %d 次", kernel.writes)
	}
	if r.SnapshotChecksum == "" || r.ConfigVersion != m.ConfigVersion {
		t.Fatalf("结果缺少清单标识: %+v", r)
	}
	// 内核最终状态必须与清单完全一致，多余 peer 必须消失。
	if _, ok := kernel.peers[stale]; ok {
		t.Fatal("清单外的 peer 未被移除")
	}
	if len(kernel.peers) != len(m.Peers) {
		t.Fatalf("最终 peer 数量不符: %d", len(kernel.peers))
	}
	// 删除动作必须排在新增/更新之后，避免中途出现无 peer 的空窗。
	last := kernel.calls[0][len(kernel.calls[0])-1]
	if !last.Remove || last.PublicKey != stale {
		t.Fatal("删除动作未排在最后")
	}
}

func TestWGApplyFailsClosedWithoutTouchingKernel(t *testing.T) {
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		setup func(t *testing.T, a *WGApplier, clk *fixedClock)
	}{
		{"租约文件不存在", func(t *testing.T, a *WGApplier, _ *fixedClock) {
			a.ManifestFile = filepath.Join(t.TempDir(), "missing.json")
		}},
		{"租约已过期", func(_ *testing.T, _ *WGApplier, clk *fixedClock) {
			clk.now = clk.now.Add(31 * time.Second)
		}},
		{"时钟早于签发时间", func(_ *testing.T, _ *WGApplier, clk *fixedClock) {
			clk.now = clk.now.Add(-time.Minute)
		}},
		{"租约文件被截断", func(t *testing.T, a *WGApplier, _ *fixedClock) {
			put(t, a.ManifestFile, []byte("{"))
		}},
		{"租约文件为空", func(t *testing.T, a *WGApplier, _ *fixedClock) {
			put(t, a.ManifestFile, nil)
		}},
		{"租约属于其他节点", func(t *testing.T, a *WGApplier, _ *fixedClock) {
			a.NodeID = "7"
		}},
		{"租约文件权限过宽", func(t *testing.T, a *WGApplier, _ *fixedClock) {
			if e := os.Chmod(a.ManifestFile, 0644); e != nil {
				t.Fatal(e)
			}
		}},
		{"未配置写入器", func(_ *testing.T, a *WGApplier, _ *fixedClock) { a.Writer = nil }},
		{"未配置读取器", func(_ *testing.T, a *WGApplier, _ *fixedClock) { a.Reader = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &fixedClock{now: base}
			m := wgManifest()
			// 内核里有一个清单外的 peer：任何失败路径都不得把它删掉。
			kernel := newFakeWG(m, map[string][]string{wgKey(200): {"10.20.0.250/32"}})
			applier, _ := applyFixture(t, kernel, clk)
			c.setup(t, applier, clk)

			if _, err := applier.Apply(context.Background()); err == nil {
				t.Fatal("应当拒绝下发但返回成功")
			}
			if kernel.writes != 0 {
				t.Fatal("失败路径仍然写入了内核")
			}
			if len(kernel.peers) != 1 {
				t.Fatal("失败路径改变了内核 peer——在线用户可能被误踢")
			}
		})
	}
}

func TestWGApplyRefusesMismatchedInterface(t *testing.T) {
	base := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*fakeWG)
		link   func(string) (*net.Interface, error)
	}{
		{"内核公钥与清单不符", func(k *fakeWG) { k.pub = wgKey(77) }, upLinkForApply},
		{"监听端口与清单不符", func(k *fakeWG) { k.port = 51821 }, upLinkForApply},
		{"接口不存在", nil, func(string) (*net.Interface, error) { return nil, errors.New("no such interface") }},
		{"接口已关闭", nil, func(name string) (*net.Interface, error) { return downLink(3, name), nil }},
		{"接口是回环", nil, func(name string) (*net.Interface, error) { return loopbackLink(3, name), nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &fixedClock{now: base}
			m := wgManifest()
			kernel := newFakeWG(m, map[string][]string{wgKey(200): {"10.20.0.250/32"}})
			if c.mutate != nil {
				c.mutate(kernel)
			}
			applier, _ := applyFixture(t, kernel, clk)
			applier.InterfaceByName = c.link

			if _, err := applier.Apply(context.Background()); err == nil {
				t.Fatal("接口与清单不符却仍然下发")
			}
			if kernel.writes != 0 {
				t.Fatal("接口校验失败仍写入了内核")
			}
		})
	}
}

func TestWGApplyReportsWriterFailure(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	kernel := newFakeWG(m, nil)
	kernel.setErr = errors.New("wg exited 1")
	applier, _ := applyFixture(t, kernel, clk)

	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("写入失败却报告成功")
	}
}

func TestWGApplyRequiresKernelToConverge(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	kernel := newFakeWG(m, nil)
	kernel.inert = true // 命令返回成功但内核没有真正变化
	applier, _ := applyFixture(t, kernel, clk)

	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("内核未收敛却报告下发成功")
	}
}

func TestWGApplyRejectsLeaseExpiringDuringApply(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	kernel := newFakeWG(m, nil)
	applier, _ := applyFixture(t, kernel, clk)
	// 下发瞬间租约到期：即便命令成功，本次下发也必须判定为无效。
	kernel.onSet = func() { clk.now = clk.now.Add(31 * time.Second) }

	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("租约在下发过程中过期却报告成功")
	}
}

func TestWGApplyRejectsInterfaceSwapDuringApply(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	kernel := newFakeWG(m, nil)
	applier, _ := applyFixture(t, kernel, clk)
	index := 3
	applier.InterfaceByName = func(name string) (*net.Interface, error) { return fakeLink(index, name), nil }
	kernel.onSet = func() { index = 9 } // 接口在下发过程中被换掉

	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("接口在下发过程中改变却报告成功")
	}
}

func TestWGApplyRefusesToRemoveNodeOwnKey(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	// 内核里出现了与节点自身公钥相同的 peer：这是异常状态，必须拒绝而不是自动删除。
	kernel := newFakeWG(m, map[string][]string{m.PublicKey: {"10.20.0.251/32"}})
	applier, _ := applyFixture(t, kernel, clk)

	if _, err := applier.Apply(context.Background()); err == nil {
		t.Fatal("节点自身公钥被当作普通 peer 处理")
	}
	if kernel.writes != 0 {
		t.Fatal("异常状态下仍执行了写入")
	}
}

func TestWGApplyRejectsMalformedKernelOutput(t *testing.T) {
	clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
	m := wgManifest()
	for _, bad := range []string{"not-a-key\t10.20.0.9/32\n", "\t10.20.0.9/32\n"} {
		kernel := &badReader{fakeWG: newFakeWG(m, nil), allowed: bad}
		applier, _ := applyFixture(t, kernel.fakeWG, clk)
		applier.Reader = kernel
		if _, err := applier.Apply(context.Background()); err == nil {
			t.Fatalf("内核输出异常 %q 却仍然下发", bad)
		}
	}
}

type badReader struct {
	*fakeWG
	allowed string
}

func (b *badReader) Show(ctx context.Context, iface, field string) ([]byte, error) {
	if field == "allowed-ips" {
		return []byte(b.allowed), nil
	}
	return b.fakeWG.Show(ctx, iface, field)
}

// ------------------------------------------------------- WGCommand.SetPeers

// dummyWG 生成一个记录调用次数的假 wg 可执行文件。
func dummyWG(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	binary, marker := filepath.Join(dir, "wg"), filepath.Join(dir, "ran")
	script := fmt.Sprintf("#!/bin/sh\necho ran >> %s\nexit 0\n", marker)
	if e := os.WriteFile(binary, []byte(script), 0755); e != nil {
		t.Fatal(e)
	}
	return binary, marker
}

func ranCount(t *testing.T, marker string) int {
	t.Helper()
	b, e := os.ReadFile(marker)
	if e != nil {
		return 0
	}
	return strings.Count(string(b), "ran")
}

func TestWGCommandSetPeersRejectsUnsafeInput(t *testing.T) {
	binary, marker := dummyWG(t)
	good := WGPeerChange{PublicKey: wgKey(2), AllowedIPs: []string{"10.20.0.2/32"}}
	cases := []struct {
		name    string
		binary  string
		iface   string
		changes []WGPeerChange
	}{
		{"二进制路径非绝对", "wg", "wg0", []WGPeerChange{good}},
		{"接口名非法", binary, "lo", []WGPeerChange{good}},
		{"接口名含空格", binary, "wg 0", []WGPeerChange{good}},
		{"变更为空", binary, "wg0", nil},
		{"公钥非法", binary, "wg0", []WGPeerChange{{PublicKey: "not-a-key", AllowedIPs: []string{"10.20.0.2/32"}}}},
		{"公钥全零", binary, "wg0", []WGPeerChange{{PublicKey: strings.Repeat("A", 43) + "=", AllowedIPs: []string{"10.20.0.2/32"}}}},
		{"同一 peer 重复", binary, "wg0", []WGPeerChange{good, {PublicKey: good.PublicKey, Remove: true}}},
		{"无 allowed-ips", binary, "wg0", []WGPeerChange{{PublicKey: wgKey(2)}}},
		{"allowed-ips 超过两个", binary, "wg0", []WGPeerChange{{PublicKey: wgKey(2), AllowedIPs: []string{"10.20.0.2/32", "10.20.0.3/32", "10.20.0.4/32"}}}},
		{"allowed-ips 为公网地址", binary, "wg0", []WGPeerChange{{PublicKey: wgKey(2), AllowedIPs: []string{"8.8.8.8/32"}}}},
		{"allowed-ips 非主机前缀", binary, "wg0", []WGPeerChange{{PublicKey: wgKey(2), AllowedIPs: []string{"10.20.0.0/24"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := ranCount(t, marker)
			w := &WGCommand{Binary: c.binary}
			if err := w.SetPeers(context.Background(), c.iface, c.changes); err == nil {
				t.Fatal("非法输入被接受")
			}
			if ranCount(t, marker) != before {
				t.Fatal("非法输入仍然执行了命令")
			}
		})
	}
}

func TestWGCommandSetPeersRunsOnValidInput(t *testing.T) {
	binary, marker := dummyWG(t)
	w := &WGCommand{Binary: binary}
	changes := []WGPeerChange{
		{PublicKey: wgKey(2), AllowedIPs: []string{"10.20.0.2/32", "fd00::2/128"}},
		{PublicKey: wgKey(3), Remove: true},
	}
	if err := w.SetPeers(context.Background(), "wg0", changes); err != nil {
		t.Fatalf("合法下发失败: %v", err)
	}
	if ranCount(t, marker) != 1 {
		t.Fatal("命令未被执行")
	}
}

func TestWGCommandSetPeersRejectsUnsafeBinary(t *testing.T) {
	dir := t.TempDir()
	// 组可写的可执行文件不得被调用。
	binary := filepath.Join(dir, "wg")
	if e := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0755); e != nil {
		t.Fatal(e)
	}
	// umask 会削掉 WriteFile 的权限位，必须显式设置才能真正造出组可写的文件。
	if e := os.Chmod(binary, 0775); e != nil {
		t.Fatal(e)
	}
	w := &WGCommand{Binary: binary}
	if err := w.SetPeers(context.Background(), "wg0", []WGPeerChange{{PublicKey: wgKey(2), AllowedIPs: []string{"10.20.0.2/32"}}}); err == nil {
		t.Fatal("组可写的可执行文件被接受")
	}
}

type inspectionHook struct {
	kernel *fakeWG
	hook   func()
}

func (r *inspectionHook) Show(ctx context.Context, iface, field string) ([]byte, error) {
	raw, err := r.kernel.Show(ctx, iface, field)
	if field == "allowed-ips" && r.hook != nil {
		f := r.hook
		r.hook = nil
		f()
	}
	return raw, err
}

func TestWGApplyRechecksLeaseBeforeWriteAndReadyReceipt(t *testing.T) {
	for _, converged := range []bool{false, true} {
		for _, change := range []string{"expired", "clock-rollback", "cancelled", "removed", "replaced"} {
			t.Run(fmt.Sprintf("converged=%v/%s", converged, change), func(t *testing.T) {
				clk := &fixedClock{now: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)}
				m := wgManifest()
				var peers map[string][]string
				if converged {
					peers = desiredPeers(m)
				}
				kernel := newFakeWG(m, peers)
				a, _ := applyFixture(t, kernel, clk)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a.Reader = &inspectionHook{kernel: kernel, hook: func() {
					switch change {
					case "expired":
						clk.now = clk.now.Add(31 * time.Second)
					case "clock-rollback":
						clk.now = clk.now.Add(-time.Second)
					case "cancelled":
						cancel()
					case "removed":
						if err := os.Remove(a.ManifestFile); err != nil {
							t.Fatal(err)
						}
					case "replaced":
						next := m
						next.ConfigVersion = "18"
						put(t, a.ManifestFile, leaseBytes(t, next, clk.now))
					}
				}}
				if _, err := a.Apply(ctx); err == nil {
					t.Fatal("stale lease reported success")
				}
				if kernel.writes != 0 {
					t.Fatalf("stale authority caused %d kernel writes", kernel.writes)
				}
			})
		}
	}
}
