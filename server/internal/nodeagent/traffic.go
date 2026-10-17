package nodeagent

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 节点侧按凭据采集流量并上报（方案 8.5 的另一半）。
//
// 方向约定（已对真实 wireguard-tools 核对）：`wg show <iface> transfer` 每行是
//
//	<公钥>\t<rx_bytes>\t<tx_bytes>
//
// rx 是节点**收到**的字节，即用户的上行；tx 是节点**发出**的字节，即用户的下行。
// 这两个方向搞反就是计费错，因此在代码与测试里都写死这条对应关系。
//
// 内核计数器是「自 peer 建立以来的累计值」，而上报要的是窗口增量，所以必须持久保存基线。
// 两个关键规则：
//   - 计数器变小视为 peer 被重建或接口重启，此时当前值本身就是重置后的全部流量，
//     增量取当前值，而不是负数也不是零；
//   - **基线只在上报被控制面确认后才前移**。未确认就前移会永久丢掉这段流量；
//     确认前重复发送同一个 report_id，服务端按幂等键去重，不会重复计费。
type TrafficCounter struct {
	PublicKey string `json:"public_key"`
	RX        string `json:"rx"`
	TX        string `json:"tx"`
}

type WGTrafficSampler struct {
	Clock                clock.Clock
	NodeID, ManifestFile string
	Reader               WGReader
	Interface            string
	// 仅测试注入；真实运行读取操作系统接口。
	InterfaceByName func(string) (*net.Interface, error)
}

func trafficSampleError() error { return errors.New("WireGuard 流量采样失败") }

func parseCounter(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > nodewire.MaxTrafficBytes || strconv.FormatInt(n, 10) != s {
		return 0, trafficSampleError()
	}
	return n, nil
}

// Sample 读取当前累计计数器，并按租约清单把公钥映射回凭据编号。
// 只统计清单里存在的 peer：清单是控制面授权过的集合，不在其中的 peer 无法归属，
// 也不应当由本节点替任何人记账。
func (s *WGTrafficSampler) Sample(ctx context.Context) ([]TrafficCounter, map[string]string, error) {
	if s.Reader == nil || s.Clock == nil {
		return nil, nil, trafficSampleError()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := s.Clock.Now()
	raw, err := readPrivate(s.ManifestFile, 1048576)
	if err != nil {
		return nil, nil, trafficSampleError()
	}
	lease, err := nodewire.ParseWGLease(raw, s.NodeID, now)
	if err != nil {
		return nil, nil, trafficSampleError()
	}
	m := lease.Manifest
	if s.Interface != "" && s.Interface != m.Interface {
		return nil, nil, trafficSampleError()
	}
	if _, err := linkUp(s.InterfaceByName, m.Interface); err != nil {
		return nil, nil, trafficSampleError()
	}
	credentials := make(map[string]string, len(m.Peers))
	for _, p := range m.Peers {
		credentials[p.PublicKey] = p.CredentialID
	}
	out, err := s.Reader.Show(ctx, m.Interface, "transfer")
	if err != nil {
		return nil, nil, trafficSampleError()
	}
	rows, err := wgRows(out, 1000)
	if err != nil {
		return nil, nil, trafficSampleError()
	}
	counters := make([]TrafficCounter, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if len(row) != 3 || !validPublicKey(row[0]) || seen[row[0]] {
			return nil, nil, trafficSampleError()
		}
		seen[row[0]] = true
		if _, err := parseCounter(row[1]); err != nil {
			return nil, nil, err
		}
		if _, err := parseCounter(row[2]); err != nil {
			return nil, nil, err
		}
		if _, ok := credentials[row[0]]; !ok {
			continue // 不在授权清单中，无法归属
		}
		counters = append(counters, TrafficCounter{PublicKey: row[0], RX: row[1], TX: row[2]})
	}
	return counters, credentials, nil
}

// Delta 按基线求窗口增量。baseline 为上次已确认上报时的累计值。
func Delta(baseline, current []TrafficCounter, credentials map[string]string) ([]nodewire.TrafficItem, error) {
	previous := make(map[string]TrafficCounter, len(baseline))
	for _, b := range baseline {
		previous[b.PublicKey] = b
	}
	items := make([]nodewire.TrafficItem, 0, len(current))
	for _, c := range current {
		credential, ok := credentials[c.PublicKey]
		if !ok {
			continue
		}
		rx, err := parseCounter(c.RX)
		if err != nil {
			return nil, err
		}
		tx, err := parseCounter(c.TX)
		if err != nil {
			return nil, err
		}
		up, down := rx, tx
		if before, ok := previous[c.PublicKey]; ok {
			pRX, err := parseCounter(before.RX)
			if err != nil {
				return nil, err
			}
			pTX, err := parseCounter(before.TX)
			if err != nil {
				return nil, err
			}
			// 计数器回退＝peer 重建或接口重启，当前值即重置后的全部流量。
			if rx >= pRX {
				up = rx - pRX
			}
			if tx >= pTX {
				down = tx - pTX
			}
		}
		if up == 0 && down == 0 {
			continue // 无流量的 peer 不占用上报条目
		}
		items = append(items, nodewire.TrafficItem{
			CredentialID: credential,
			UpBytes:      strconv.FormatInt(up, 10),
			DownBytes:    strconv.FormatInt(down, 10),
		})
	}
	if len(items) > nodewire.MaxTrafficItems {
		return nil, trafficSampleError()
	}
	return items, nil
}
