package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 本文件是整个项目里第一处会真正改写内核状态的代码，因此边界写死在这里：
//
//  1. 只动 peer。接口私钥、监听端口、地址、路由一概不碰——那些由部署方配置，
//     控制面无权改。接口自身与清单不符时拒绝下发，而不是"纠正"它。
//  2. 失败闭合。租约读不到、解析失败或已过期时立即返回错误，**不删除任何 peer**；
//     宁可维持现状，也不能因为一次取件失败把在线用户全部踢下线。
//  3. 幂等。内核状态已与清单一致时不执行任何写入。
//  4. 下发前后都复核租约时效与接口身份；期间任一条件失效即判定本次下发无效。
//  5. 所有公钥与地址在构造命令前重新校验一次，绝不把未经校验的字符串交给 exec。
//
// 调用方必须自行保证开关已开启（config.NodeSync.ApplyEnabled，默认 false）。

// WGPeerChange 描述对单个 peer 的一次变更。Remove 为真时忽略 AllowedIPs。
type WGPeerChange struct {
	PublicKey  string
	AllowedIPs []string
	Remove     bool
}

// WGWriter 把 peer 变更写入接口。生产实现是 WGCommand，测试注入替身。
type WGWriter interface {
	SetPeers(ctx context.Context, iface string, changes []WGPeerChange) error
}

// WGApplyResult 汇报本次下发的结果，供上层记录日志与上报。
type WGApplyResult struct {
	Interface        string
	ConfigVersion    string
	SnapshotChecksum string
	Added            int
	Updated          int
	Removed          int
}

func (r WGApplyResult) Changed() bool { return r.Added+r.Updated+r.Removed > 0 }

type WGApplier struct {
	Clock                clock.Clock
	NodeID, ManifestFile string
	Reader               WGReader
	Writer               WGWriter
	// 仅测试注入；真实运行读取操作系统接口。
	InterfaceByName func(string) (*net.Interface, error)
}

func wgApplyError() error { return errors.New("WireGuard peer 下发失败") }

// currentPeers 读取内核当前的 peer 与 allowed-ips。
func (a *WGApplier) currentPeers(ctx context.Context, iface string) (map[string][]string, error) {
	raw, e := a.Reader.Show(ctx, iface, "allowed-ips")
	if e != nil {
		return nil, wgApplyError()
	}
	rows, e := wgRows(raw, 1000)
	if e != nil {
		return nil, wgApplyError()
	}
	out := make(map[string][]string, len(rows))
	for _, row := range rows {
		if len(row) < 1 || len(row) > 3 {
			return nil, wgApplyError()
		}
		if !validPublicKey(row[0]) {
			return nil, wgApplyError()
		}
		if _, seen := out[row[0]]; seen {
			return nil, wgApplyError()
		}
		ips := append([]string(nil), row[1:]...)
		sort.Strings(ips)
		out[row[0]] = ips
	}
	return out, nil
}

// checkInterface 确认接口存在、已启用，且其公钥与监听端口与清单一致。
// 不一致时拒绝下发：那说明这台机器上的接口不是清单描述的那一个。
func (a *WGApplier) checkInterface(ctx context.Context, m nodewire.WGManifest) (int, error) {
	index, e := linkUp(a.InterfaceByName, m.Interface)
	if e != nil {
		return 0, wgApplyError()
	}
	for _, field := range []string{"public-key", "listen-port"} {
		raw, e := a.Reader.Show(ctx, m.Interface, field)
		if e != nil {
			return 0, wgApplyError()
		}
		wanted := m.PublicKey
		if field == "listen-port" {
			wanted = strconv.Itoa(m.ListenPort)
		}
		if strings.TrimRight(string(raw), "\n") != wanted {
			return 0, wgApplyError()
		}
	}
	return index, nil
}

// plan 求内核现状与清单期望之间的最小差集。
func plan(desired nodewire.WGManifest, current map[string][]string) ([]WGPeerChange, WGApplyResult) {
	var changes []WGPeerChange
	result := WGApplyResult{Interface: desired.Interface, ConfigVersion: desired.ConfigVersion}
	wanted := map[string]bool{}
	for _, p := range desired.Peers {
		wanted[p.PublicKey] = true
		ips := append([]string(nil), p.AllowedIPs...)
		sort.Strings(ips)
		have, present := current[p.PublicKey]
		if present && strings.Join(have, ",") == strings.Join(ips, ",") {
			continue
		}
		if present {
			result.Updated++
		} else {
			result.Added++
		}
		changes = append(changes, WGPeerChange{PublicKey: p.PublicKey, AllowedIPs: ips})
	}
	removals := make([]string, 0, len(current))
	for k := range current {
		if !wanted[k] {
			removals = append(removals, k)
		}
	}
	sort.Strings(removals)
	for _, k := range removals {
		result.Removed++
		changes = append(changes, WGPeerChange{PublicKey: k, Remove: true})
	}
	return changes, result
}

// Apply 读取租约文件并把其中的 peer 下发到内核。返回结果的 Changed() 为假表示本次无需写入。
func (a *WGApplier) Apply(ctx context.Context) (WGApplyResult, error) {
	var out WGApplyResult
	if a.Reader == nil || a.Writer == nil || a.Clock == nil {
		return out, wgApplyError()
	}
	start := a.Clock.Now()
	raw, e := readPrivate(a.ManifestFile, 1048576)
	if e != nil {
		return out, wgApplyError()
	}
	lease, e := nodewire.ParseWGLease(raw, a.NodeID, start)
	if e != nil {
		return out, wgApplyError()
	}
	m := lease.Manifest

	index, e := a.checkInterface(ctx, m)
	if e != nil {
		return out, e
	}
	current, e := a.currentPeers(ctx, m.Interface)
	if e != nil {
		return out, e
	}
	changes, result := plan(m, current)
	result.SnapshotChecksum = lease.Checksum
	// Kernel inspection can block. A lease that was valid before those reads
	// must not authorize a write (or a ready receipt) after its deadline, after
	// cancellation, or after the synchronizer has replaced/removed the file.
	if e := a.currentLease(ctx, raw, start); e != nil {
		return out, e
	}
	if len(changes) == 0 {
		return result, nil
	}
	// 构造命令前重新校验，绝不把未经校验的字符串交给 exec。
	for _, c := range changes {
		if !validPublicKey(c.PublicKey) || c.PublicKey == m.PublicKey {
			return out, wgApplyError()
		}
		if c.Remove {
			continue
		}
		if len(c.AllowedIPs) < 1 || len(c.AllowedIPs) > 2 {
			return out, wgApplyError()
		}
		for _, ip := range c.AllowedIPs {
			if !validPeerIP(ip) {
				return out, wgApplyError()
			}
		}
	}
	if e := a.currentLease(ctx, raw, start); e != nil {
		return out, e
	}
	if e := a.Writer.SetPeers(ctx, m.Interface, changes); e != nil {
		return out, wgApplyError()
	}

	// 下发后复核：接口没被换掉，内核状态确实收敛到清单，且租约仍在有效期内。
	again, e := a.checkInterface(ctx, m)
	if e != nil || again != index {
		return out, wgApplyError()
	}
	settled, e := a.currentPeers(ctx, m.Interface)
	if e != nil {
		return out, e
	}
	if remaining, _ := plan(m, settled); len(remaining) != 0 {
		return out, wgApplyError()
	}
	if e := a.currentLease(ctx, raw, start); e != nil {
		return out, e
	}
	return result, nil
}

func (a *WGApplier) currentLease(ctx context.Context, expected []byte, start time.Time) error {
	now := a.Clock.Now()
	if ctx.Err() != nil || now.Before(start) {
		return wgApplyError()
	}
	if _, err := nodewire.ParseWGLease(expected, a.NodeID, now); err != nil {
		return wgApplyError()
	}
	current, err := readPrivate(a.ManifestFile, 1048576)
	if err != nil || !bytes.Equal(current, expected) {
		return wgApplyError()
	}
	return nil
}
