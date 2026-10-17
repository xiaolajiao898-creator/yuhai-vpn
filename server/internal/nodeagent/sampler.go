package nodeagent

import (
	"context"
	"errors"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type RuntimeStatus struct {
	ReadyProtocols   []string `json:"ready_protocols"`
	Schema           int      `json:"schema"`
	NodeID           string   `json:"node_id"`
	ObservedAt       string   `json:"observed_at"`
	ConfigVersion    string   `json:"config_version"`
	SnapshotChecksum string   `json:"snapshot_checksum"`
	OnlineCount      *int64   `json:"online_count"`
}

var utcTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?Z$`)
var checksum = regexp.MustCompile(`^[a-f0-9]{64}$`)

func readRuntime(path, nodeID string, now time.Time) (RuntimeStatus, error) {
	raw, err := readPrivate(path, 4096)
	if err != nil {
		return RuntimeStatus{}, err
	}
	return parseRuntime(raw, nodeID, now)
}
func parseRuntime(raw []byte, nodeID string, now time.Time) (RuntimeStatus, error) {
	var s RuntimeStatus
	if strictJSON(raw, &s) != nil || s.Schema != 2 || len(s.ReadyProtocols) == 0 || !nodewire.ValidProtocols(s.ReadyProtocols) || s.NodeID != nodeID || s.OnlineCount == nil || !utcTime.MatchString(s.ObservedAt) {
		return s, errors.New("invalid runtime status")
	}
	observed, err := time.Parse(time.RFC3339Nano, s.ObservedAt)
	if err != nil || observed.After(now) || now.Sub(observed) > 10*time.Second {
		return s, errors.New("runtime status stale")
	}
	version, err := integer(s.ConfigVersion)
	if err != nil || version == 0 || !checksum.MatchString(s.SnapshotChecksum) || *s.OnlineCount < 0 || *s.OnlineCount > 10000000 {
		return s, errors.New("runtime configuration not ready")
	}
	return s, nil
}

type counters struct{ total, idle, rx, tx uint64 }

func bounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(b) > 65536 {
		return nil, errors.New("system metric too large")
	}
	return b, nil
}
func parseCPU(raw []byte) (uint64, uint64, error) {
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		if len(f) < 9 {
			return 0, 0, errors.New("incomplete CPU counters")
		}
		sum := uint64(0)
		idle := uint64(0)
		for i := 1; i <= 8; i++ {
			v, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil || ^uint64(0)-sum < v {
				return 0, 0, errors.New("invalid CPU counter")
			}
			sum += v
			if i == 4 || i == 5 {
				idle += v
			}
		}
		return sum, idle, nil
	}
	return 0, 0, errors.New("CPU counters missing")
}
func parseNet(raw []byte, iface string) (uint64, uint64, error) {
	for _, line := range strings.Split(string(raw), "\n") {
		name, values, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(values)
		if len(f) != 16 {
			return 0, 0, errors.New("invalid network counters")
		}
		a, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		b, err := strconv.ParseUint(f[8], 10, 64)
		return a, b, err
	}
	return 0, 0, errors.New("network interface missing")
}
func ratio(n, d uint64, scale uint64) (int64, error) {
	if d == 0 {
		return 0, errors.New("zero metric interval")
	}
	v := new(big.Int).SetUint64(n)
	v.Mul(v, new(big.Int).SetUint64(scale))
	v.Add(v, new(big.Int).SetUint64(d-1))
	v.Div(v, new(big.Int).SetUint64(d))
	if !v.IsInt64() {
		return 0, errors.New("metric overflow")
	}
	return v.Int64(), nil
}
func parseMemory(raw []byte) (int, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || (f[0] != "MemTotal:" && f[0] != "MemAvailable:") {
			continue
		}
		if _, ok := values[f[0]]; ok || len(f) != 3 || f[2] != "kB" {
			return 0, errors.New("invalid memory counters")
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			return 0, err
		}
		values[f[0]] = v
	}
	total, ok := values["MemTotal:"]
	avail, have := values["MemAvailable:"]
	if !ok || !have || total == 0 || avail > total {
		return 0, errors.New("missing memory counters")
	}
	v, err := ratio(total-avail, total, 100)
	return int(v), err
}
func rates(a, b counters, elapsed time.Duration) (int, string, string, error) {
	if elapsed < 500*time.Millisecond || elapsed > 5*time.Second || b.total <= a.total || b.idle < a.idle || b.rx < a.rx || b.tx < a.tx || b.idle-a.idle > b.total-a.total {
		return 0, "", "", errors.New("counter reset or invalid sampling interval")
	}
	cpu, err := ratio((b.total-a.total)-(b.idle-a.idle), b.total-a.total, 100)
	if err != nil {
		return 0, "", "", err
	}
	rx, err := ratio(b.rx-a.rx, uint64(elapsed), 8000000000)
	if err != nil {
		return 0, "", "", err
	}
	tx, err := ratio(b.tx-a.tx, uint64(elapsed), 8000000000)
	return int(cpu), strconv.FormatInt(rx, 10), strconv.FormatInt(tx, 10), err
}

type LinuxSampler struct {
	Clock                          clock.Clock
	NodeID, Interface, RuntimeFile string
	ProcRoot                       string
}

func (s *LinuxSampler) readCounters() (counters, error) {
	var c counters
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	cpu, e := bounded(filepath.Join(root, "stat"))
	if e != nil {
		return c, e
	}
	c.total, c.idle, e = parseCPU(cpu)
	if e != nil {
		return c, e
	}
	net, e := bounded(filepath.Join(root, "net/dev"))
	if e != nil {
		return c, e
	}
	c.rx, c.tx, e = parseNet(net, s.Interface)
	return c, e
}
func (s *LinuxSampler) Sample(ctx context.Context) (nodewire.Heartbeat, error) {
	var h nodewire.Heartbeat
	a, err := s.readCounters()
	if err != nil {
		return h, err
	}
	start := s.Clock.Now()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return h, ctx.Err()
	case <-timer.C:
	}
	b, err := s.readCounters()
	if err != nil {
		return h, err
	}
	h.CPU, h.NetRX, h.NetTX, err = rates(a, b, s.Clock.Now().Sub(start))
	if err != nil {
		return h, err
	}
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	mem, err := bounded(filepath.Join(root, "meminfo"))
	if err != nil {
		return h, err
	}
	h.Memory, err = parseMemory(mem)
	if err != nil {
		return h, err
	}
	kernel, err := bounded(filepath.Join(root, "sys/kernel/osrelease"))
	if err != nil {
		return h, err
	}
	h.Kernel = "Linux " + strings.TrimSpace(string(kernel))
	runtime, err := readRuntime(s.RuntimeFile, s.NodeID, s.Clock.Now())
	if err != nil {
		return h, err
	}
	h.AgentVersion = "0.2.0"
	h.ReadyProtocols = runtime.ReadyProtocols
	h.ConfigVersion = runtime.ConfigVersion
	h.SnapshotChecksum = runtime.SnapshotChecksum
	h.OnlineCount = *runtime.OnlineCount
	return h, nil
}
