package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WGReader is intentionally read-only; no shell or configuration operations.
type WGReader interface {
	Show(context.Context, string, string) ([]byte, error)
}
type WGObserver struct {
	Clock                clock.Clock
	NodeID, ManifestFile string
	Reader               WGReader
	// Injected only in tests. Real observation reads the OS interface.
	InterfaceByName func(string) (*net.Interface, error)
}

func wgObservationError() error { return errors.New("WireGuard runtime verification failed") }

// linkUp 校验接口存在、名称一致、已启用且不是回环，返回其索引。
// observer 与 applier 共用，保证两者对"接口可用"的判定完全一致。
func linkUp(lookup func(string) (*net.Interface, error), name string) (int, error) {
	if lookup == nil {
		lookup = net.InterfaceByName
	}
	v, e := lookup(name)
	if e != nil || v == nil || v.Name != name || v.Index <= 0 || v.Flags&net.FlagUp == 0 || v.Flags&net.FlagLoopback != 0 {
		return 0, wgObservationError()
	}
	return v.Index, nil
}
func (o *WGObserver) link(name string) (int, error) { return linkUp(o.InterfaceByName, name) }
func wgRows(raw []byte, max int) ([][]string, error) {
	if len(raw) > 262144 {
		return nil, wgObservationError()
	}
	if len(raw) == 0 {
		return [][]string{}, nil
	}
	// Machine format must be complete, with exactly one newline per record.
	if raw[len(raw)-1] != '\n' {
		return nil, wgObservationError()
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	if len(lines) > max {
		return nil, wgObservationError()
	}
	out := make([][]string, 0, len(lines))
	for _, line := range lines {
		if strings.ContainsAny(line, "\r\x00") {
			return nil, wgObservationError()
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return nil, wgObservationError()
		}
		out = append(out, fields)
	}
	return out, nil
}
func (o *WGObserver) matchConfig(ctx context.Context, m WGManifest) error {
	for _, field := range []string{"public-key", "listen-port"} {
		raw, e := o.Reader.Show(ctx, m.Interface, field)
		if e != nil {
			return wgObservationError()
		}
		wanted := m.PublicKey
		if field == "listen-port" {
			wanted = strconv.Itoa(m.ListenPort)
		}
		if string(raw) != wanted+"\n" {
			return wgObservationError()
		}
	}
	raw, e := o.Reader.Show(ctx, m.Interface, "allowed-ips")
	if e != nil {
		return wgObservationError()
	}
	rows, e := wgRows(raw, 1000)
	if e != nil || len(rows) != len(m.Peers) {
		return wgObservationError()
	}
	expected := map[string][]string{}
	for _, p := range m.Peers {
		expected[p.PublicKey] = p.AllowedIPs
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if len(row) < 2 || len(row) > 3 || seen[row[0]] {
			return wgObservationError()
		}
		wanted, ok := expected[row[0]]
		if !ok {
			return wgObservationError()
		}
		seen[row[0]] = true
		ips := append([]string(nil), row[1:]...)
		sort.Strings(ips)
		if strings.Join(ips, ",") != strings.Join(wanted, ",") {
			return wgObservationError()
		}
	}
	return nil
}
func (o *WGObserver) Observe(ctx context.Context) (RuntimeStatus, error) {
	var out RuntimeStatus
	if o.Reader == nil || o.Clock == nil {
		return out, wgObservationError()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := o.Clock.Now()
	raw, e := readPrivate(o.ManifestFile, 1048576)
	if e != nil {
		return out, wgObservationError()
	}
	lease, e := nodewire.ParseWGLease(raw, o.NodeID, start)
	m, digest := lease.Manifest, lease.Checksum
	if e != nil {
		return out, wgObservationError()
	}
	index, e := o.link(m.Interface)
	if e != nil {
		return out, e
	}
	if e = o.matchConfig(ctx, m); e != nil {
		return out, e
	}
	handshakes, e := o.Reader.Show(ctx, m.Interface, "latest-handshakes")
	if e != nil {
		return out, wgObservationError()
	}
	rows, e := wgRows(handshakes, 1000)
	if e != nil || len(rows) != len(m.Peers) {
		return out, wgObservationError()
	}
	now := o.Clock.Now()
	online := int64(0)
	expected := map[string]bool{}
	for _, p := range m.Peers {
		expected[p.PublicKey] = true
	}
	for _, row := range rows {
		if len(row) != 2 || !expected[row[0]] {
			return out, wgObservationError()
		}
		delete(expected, row[0])
		ts, err := integer(row[1])
		if err != nil || ts > now.Unix() {
			return out, wgObservationError()
		}
		if ts != 0 && now.Sub(time.Unix(ts, 0)) <= 180*time.Second {
			online++
		}
	}
	if e = o.matchConfig(ctx, m); e != nil {
		return out, e
	}
	again, e := o.link(m.Interface)
	if e != nil || again != index {
		return out, wgObservationError()
	}
	final, e := readPrivate(o.ManifestFile, 1048576)
	if e != nil || !bytes.Equal(raw, final) {
		return out, wgObservationError()
	}
	end := o.Clock.Now()
	if _, e = nodewire.ParseWGLease(final, o.NodeID, end); e != nil {
		return out, wgObservationError()
	}
	if ctx.Err() != nil || end.Before(start) || end.Sub(start) > 5*time.Second || end.Before(now) {
		return out, wgObservationError()
	}
	out = RuntimeStatus{Schema: 2, NodeID: m.NodeID, ObservedAt: end.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ConfigVersion: m.ConfigVersion, SnapshotChecksum: digest, OnlineCount: &online, ReadyProtocols: []string{"WIREGUARD"}}
	return out, nil
}
