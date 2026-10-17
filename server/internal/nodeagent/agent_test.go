package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

type sampleFunc func(context.Context) (nodewire.Heartbeat, error)

func (f sampleFunc) Sample(c context.Context) (nodewire.Heartbeat, error) { return f(c) }

type sendFunc func(context.Context, nodewire.Heartbeat) (nodewire.Receipt, error)

func (f sendFunc) Send(c context.Context, h nodewire.Heartbeat) (nodewire.Receipt, error) {
	return f(c, h)
}
func sample() nodewire.Heartbeat {
	return nodewire.Heartbeat{AgentVersion: "0.1.0", ConfigVersion: "1", SnapshotChecksum: strings.Repeat("b", 64), CPU: 10, Memory: 20, NetRX: "9007199254740993", NetTX: "0", OnlineCount: 7, Kernel: "Linux 6.8"}
}
func fixtureStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := InitStore(dir, "7", "https://node.invalid/node/v1/heartbeat"); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir, "7", "https://node.invalid/node/v1/heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func TestAgentStoreLockCorruptionAndIdentity(t *testing.T) {
	s := fixtureStore(t)
	if _, err := OpenStore(s.dir, "7", s.state.Endpoint); err == nil {
		t.Fatal("double open allowed")
	}
	if err := InitStore(s.dir, "7", s.state.Endpoint); err == nil {
		t.Fatal("existing state reset")
	}
	endpoint := s.state.Endpoint
	dir := s.dir
	s.Close()
	if _, err := OpenStore(dir, "8", endpoint); err == nil {
		t.Fatal("wrong node accepted")
	}
	if _, err := OpenStore(dir, "7", "https://other.invalid"); err == nil {
		t.Fatal("wrong endpoint accepted")
	}
	path := filepath.Join(dir, "state.json")
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"version":1,"version":1}`, strings.Replace(string(old), `"pending":null`, `"PENDING":null`, 1), strings.Replace(string(old), `"last_sequence":"0"`, `"last_sequence":"01"`, 1), string(old) + `{}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(dir, "7", endpoint); err == nil {
			t.Fatal("corrupt state accepted", raw)
		}
	}
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, "7", endpoint); err == nil {
		t.Fatal("public state accepted")
	}
	_ = os.Chmod(path, 0600)
	moved := path + ".old"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, "7", endpoint); err == nil {
		t.Fatal("missing state reinitialized")
	}
	if err := os.Symlink(moved, path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, "7", endpoint); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestAgentDurableRetryRestartAndSequence(t *testing.T) {
	s := fixtureStore(t)
	clk := &fixedClock{time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)}
	samples := 0
	payloads := []string{}
	sampler := sampleFunc(func(context.Context) (nodewire.Heartbeat, error) { samples++; return sample(), nil })
	sender := sendFunc(func(_ context.Context, h nodewire.Heartbeat) (nodewire.Receipt, error) {
		b, _ := json.Marshal(h)
		payloads = append(payloads, string(b))
		return nodewire.Receipt{}, &DeliveryError{Kind: "transient"}
	})
	r := &Runner{Store: s, Sampler: sampler, Sender: sender, Clock: clk}
	out, err := r.Step(context.Background())
	if err != nil || out.State != "pending" || s.state.Pending == nil {
		t.Fatal(out, err)
	}
	dir, ep := s.dir, s.state.Endpoint
	s.Close()
	s, err = OpenStore(dir, "7", ep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r.Store = s
	clk.now = clk.now.Add(time.Minute)
	if _, err = r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if samples != 1 || len(payloads) != 2 || payloads[0] != payloads[1] {
		t.Fatal("restart resampled uncertain request")
	}
	r.Sender = sendFunc(func(_ context.Context, h nodewire.Heartbeat) (nodewire.Receipt, error) {
		return nodewire.Receipt{Accepted: true, Sequence: h.Sequence, Duplicate: true}, nil
	})
	if out, err = r.Step(context.Background()); err != nil || out.State != "accepted" || s.state.Pending != nil || s.state.LastSequence != "1" {
		t.Fatal(out, err)
	}
	r.Sender = sendFunc(func(context.Context, nodewire.Heartbeat) (nodewire.Receipt, error) {
		return nodewire.Receipt{}, &DeliveryError{Kind: "expired_request", Code: 40105}
	})
	if out, err = r.Step(context.Background()); err != nil || out.State != "expired" || s.state.Pending != nil || s.state.LastSequence != "2" {
		t.Fatal(out, err)
	}
	r.Sender = sendFunc(func(context.Context, nodewire.Heartbeat) (nodewire.Receipt, error) {
		return nodewire.Receipt{}, &DeliveryError{Kind: "rejected", Code: 40310}
	})
	if _, err = r.Step(context.Background()); err == nil || s.state.Pending == nil || s.state.Pending.Sequence != "3" {
		t.Fatal("fatal response lost pending request", err)
	}
}
func TestAgentPreparationFailureNeverSends(t *testing.T) {
	s := fixtureStore(t)
	calls := 0
	r := &Runner{Store: s, Clock: &fixedClock{time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)}, Sampler: sampleFunc(func(context.Context) (nodewire.Heartbeat, error) { return sample(), nil }), Sender: sendFunc(func(context.Context, nodewire.Heartbeat) (nodewire.Receipt, error) {
		calls++
		return nodewire.Receipt{}, nil
	})}
	path := filepath.Join(s.dir, "state.json")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Step(context.Background()); err == nil || calls != 0 {
		t.Fatal("sent before durable preparation", err, calls)
	}
}
func TestAgentConcurrencyAndMissingSampler(t *testing.T) {
	s := fixtureStore(t)
	r := &Runner{Store: s, Clock: &fixedClock{time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)}, Sampler: sampleFunc(func(context.Context) (nodewire.Heartbeat, error) { return sample(), nil }), Sender: sendFunc(func(_ context.Context, h nodewire.Heartbeat) (nodewire.Receipt, error) {
		return nodewire.Receipt{Accepted: true, Sequence: h.Sequence}, nil
	})}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := r.Step(context.Background()); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.state.LastSequence != "8" {
		t.Fatal("sequence race", s.state.LastSequence)
	}
	r.Sampler = sampleFunc(func(context.Context) (nodewire.Heartbeat, error) {
		return nodewire.Heartbeat{}, errors.New("no runtime source")
	})
	if out, err := r.Step(context.Background()); err != nil || out.State != "sampling_failed" || s.state.LastSequence != "8" {
		t.Fatal(out, err)
	}
}
func TestAgentOSCountersAndRuntimeFreshness(t *testing.T) {
	total, idle, err := parseCPU([]byte("cpu 10 20 30 40 50 60 70 80 999 999\ncpu0 2"))
	if err != nil || total != 360 || idle != 90 {
		t.Fatal("guest double count", total, idle, err)
	}
	if mem, err := parseMemory([]byte("MemTotal: 1000 kB\nMemAvailable: 666 kB\n")); err != nil || mem != 34 {
		t.Fatal(mem, err)
	}
	if _, err := parseMemory([]byte("MemTotal: 1000 kB")); err == nil {
		t.Fatal("missing availability invented")
	}
	net := []byte(" lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n eth0: 9007199254740993 0 0 0 0 0 0 0 700 0 0 0 0 0 0 0")
	rx, tx, err := parseNet(net, "eth0")
	if err != nil || rx != 9007199254740993 || tx != 700 {
		t.Fatal(rx, tx, err)
	}
	a := counters{100, 50, 9007199254740993, 100}
	b := counters{200, 75, 9007199254741093, 125}
	cpu, in, out, err := rates(a, b, time.Second)
	if err != nil || cpu != 75 || in != "800" || out != "200" {
		t.Fatal(cpu, in, out, err)
	}
	for _, bad := range []counters{{99, 50, 100, 100}, {200, 49, 100, 100}, {200, 190, a.rx, 100}, {200, 60, a.rx - 1, 100}} {
		if _, _, _, err := rates(a, bad, time.Second); err == nil {
			t.Fatal("reset accepted")
		}
	}
	now := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	n := int64(5)
	status := RuntimeStatus{Schema: 2, ReadyProtocols: []string{"WIREGUARD"}, NodeID: "7", ObservedAt: now.Format(time.RFC3339Nano), ConfigVersion: "1", SnapshotChecksum: strings.Repeat("b", 64), OnlineCount: &n}
	path := filepath.Join(t.TempDir(), "runtime.json")
	raw, _ := json.Marshal(status)
	_ = os.WriteFile(path, raw, 0600)
	if _, err := readRuntime(path, "7", now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(11 * time.Second), now.Add(-time.Second)} {
		if _, err := readRuntime(path, "7", at); err == nil {
			t.Fatal("stale/future runtime accepted")
		}
	}
	for _, bad := range []string{strings.Replace(string(raw), `"online_count":5`, `"online_count":null`, 1), strings.Replace(string(raw), `"config_version":"1"`, `"config_version":"0"`, 1), strings.Replace(string(raw), `"schema":2`, `"SCHEMA":2`, 1)} {
		_ = os.WriteFile(path, []byte(bad), 0600)
		if _, err := readRuntime(path, "7", now); err == nil {
			t.Fatal("invalid runtime accepted")
		}
	}
}
func TestAgentEndpointAndStrictReceiptShape(t *testing.T) {
	for _, raw := range []string{"http://node.invalid", "https://u:p@node.invalid", "https://node.invalid/path", "https://node.invalid?x=1", "https://node.invalid/#x", "https://node.invalid:0"} {
		if _, err := Endpoint(raw); err == nil {
			t.Fatal("unsafe endpoint", raw)
		}
	}
	if endpoint, err := Endpoint("https://node.invalid:8443/"); err != nil || endpoint != "https://node.invalid:8443/node/v1/heartbeat" {
		t.Fatal(endpoint, err)
	}
	var receipt nodewire.Receipt
	valid := `{"accepted":true,"duplicate":false,"sequence":"1","received_at":"2032-01-01T00:00:00Z","server_time":"2032-01-01T00:00:00Z","actions":[]}`
	if err := strictJSON([]byte(valid), &receipt); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(valid, `"duplicate":false,`, "", 1), strings.Replace(valid, `"duplicate":false`, `"DUPLICATE":false`, 1), strings.Replace(valid, `"accepted":true`, `"accepted":true,"accepted":false`, 1)} {
		if err := strictJSON([]byte(bad), &receipt); err == nil {
			t.Fatal("ambiguous receipt accepted")
		}
	}
}
