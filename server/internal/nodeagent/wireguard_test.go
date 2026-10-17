package nodeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func wgKey(n byte) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string([]byte{n}), 32)))
}
func wgManifest() WGManifest {
	return WGManifest{Schema: 1, NodeID: "9007199254740993", ConfigVersion: "17", Interface: "wg0", PublicKey: wgKey(1), ListenPort: 51820, Peers: []WGPeer{{CredentialID: "9007199254740993", PublicKey: wgKey(2), AllowedIPs: []string{"fd00::2/128", "10.20.0.2/32"}}, {CredentialID: "2", PublicKey: wgKey(3), AllowedIPs: []string{"10.20.0.3/32"}}}}
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func put(t *testing.T, path string, b []byte) {
	t.Helper()
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestWGManifestExactCanonicalAndInvalidInputs(t *testing.T) {
	m := wgManifest()
	raw := encode(t, m)
	parsed, digest, e := parseWGManifest(raw, m.NodeID)
	if e != nil || parsed.Peers[0].CredentialID != "2" || len(digest) != 64 {
		t.Fatal(parsed, e)
	}
	reversed := m
	reversed.Peers = []WGPeer{m.Peers[1], m.Peers[0]}
	_, other, e := parseWGManifest(encode(t, reversed), m.NodeID)
	if e != nil || other != digest {
		t.Fatal("reordering changed canonical digest")
	}
	for _, modify := range []func(*WGManifest){
		func(v *WGManifest) { v.Schema = 2 }, func(v *WGManifest) { v.NodeID = "9007199254740992" }, func(v *WGManifest) { v.ConfigVersion = "01" }, func(v *WGManifest) { v.ConfigVersion = "9223372036854775808" }, func(v *WGManifest) { v.Interface = "all" }, func(v *WGManifest) { v.Interface = "wg0;id" }, func(v *WGManifest) { v.ListenPort = 0 }, func(v *WGManifest) { v.PublicKey = wgKey(0) }, func(v *WGManifest) { v.Peers = nil },
		func(v *WGManifest) { v.Peers[0].CredentialID = "2" }, func(v *WGManifest) { v.Peers[0].PublicKey = v.PublicKey }, func(v *WGManifest) { v.Peers[0].PublicKey = v.Peers[1].PublicKey }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"10.20.0.0/24"} }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"0.0.0.0/0"} }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"8.8.8.8/32"} }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"FD00::2/128"} }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"10.20.0.3/32"} }, func(v *WGManifest) { v.Peers[0].AllowedIPs = []string{"10.20.0.2/32", "10.20.0.2/32"} },
	} {
		v := wgManifest()
		modify(&v)
		if _, _, e := parseWGManifest(encode(t, v), m.NodeID); e == nil {
			t.Fatal("bad manifest accepted")
		}
	}
	for _, bad := range []string{strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1), strings.Replace(string(raw), `"schema":1`, `"Schema":1`, 1), strings.Replace(string(raw), `"listen_port":51820`, `"listen_port":null`, 1), string(raw) + `{}`, strings.Replace(string(raw), `"schema":1,`, "", 1)} {
		if _, _, e := parseWGManifest([]byte(bad), m.NodeID); e == nil {
			t.Fatal("bad JSON accepted")
		}
	}
	m.Peers = []WGPeer{}
	if _, _, e := parseWGManifest(encode(t, m), m.NodeID); e != nil {
		t.Fatal("valid empty snapshot", e)
	}
}

type wgReadFunc func(context.Context, string, string) ([]byte, error)

func (f wgReadFunc) Show(c context.Context, i, p string) ([]byte, error) { return f(c, i, p) }
func wgFixture(t *testing.T) (*WGObserver, *WGManifest, *fixedClock, map[string]string) {
	t.Helper()
	m := wgManifest()
	clk := &fixedClock{now: time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "manifest.json")
	put(t, path, leaseBytes(t, m, clk.now))
	data := map[string]string{"public-key": m.PublicKey + "\n", "listen-port": "51820\n", "allowed-ips": m.Peers[1].PublicKey + "\t10.20.0.3/32\n" + m.Peers[0].PublicKey + "\tfd00::2/128 10.20.0.2/32\n", "latest-handshakes": fmt.Sprintf("%s\t%d\n%s\t0\n", m.Peers[0].PublicKey, clk.now.Add(-180*time.Second).Unix(), m.Peers[1].PublicKey)}
	observer := &WGObserver{Clock: clk, NodeID: m.NodeID, ManifestFile: path, InterfaceByName: func(s string) (*net.Interface, error) {
		return &net.Interface{Index: 9, Name: s, Flags: net.FlagUp}, nil
	}, Reader: wgReadFunc(func(_ context.Context, i, f string) ([]byte, error) {
		if i != m.Interface {
			t.Fatal("wrong interface")
		}
		v, ok := data[f]
		if !ok {
			t.Fatal("unapproved command", f)
		}
		return []byte(v), nil
	})}
	return observer, &m, clk, data
}
func TestWGObservationRejectsDriftAndCountsRecentPeers(t *testing.T) {
	o, m, clk, data := wgFixture(t)
	out, e := o.Observe(context.Background())
	if e != nil || *out.OnlineCount != 1 || out.Schema != 2 || out.ConfigVersion != "17" || strings.Join(out.ReadyProtocols, ",") != "WIREGUARD" {
		t.Fatal(out, e)
	}
	clk.now = clk.now.Add(time.Second)
	out, e = o.Observe(context.Background())
	if e != nil || *out.OnlineCount != 0 {
		t.Fatal("old handshake counted", e)
	}
	for _, field := range []string{"public-key", "listen-port", "allowed-ips", "latest-handshakes"} {
		original := data[field]
		for _, bad := range []string{"", original + original, strings.TrimSuffix(original, "\n"), "untrusted-value\n"} {
			data[field] = bad
			if _, e = o.Observe(context.Background()); e == nil {
				t.Fatal("bad observation accepted", field)
			}
		}
		data[field] = original
	}
	data["latest-handshakes"] = fmt.Sprintf("%s\t%d\n%s\t0\n", m.Peers[0].PublicKey, clk.now.Add(time.Second).Unix(), m.Peers[1].PublicKey)
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("future handshake accepted")
	}
	o, _, _, _ = wgFixture(t)
	original := o.Reader
	count := 0
	o.Reader = wgReadFunc(func(ctx context.Context, i, f string) ([]byte, error) {
		count++
		if count == 5 {
			return []byte(wgKey(8) + "\n"), nil
		}
		return original.Show(ctx, i, f)
	})
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("configuration changed between reads")
	}
	o, m, _, _ = wgFixture(t)
	original = o.Reader
	count = 0
	o.Reader = wgReadFunc(func(ctx context.Context, i, f string) ([]byte, error) {
		count++
		if count == 7 {
			m.ConfigVersion = "18"
			put(t, o.ManifestFile, leaseBytes(t, m, o.Clock.Now()))
		}
		return original.Show(ctx, i, f)
	})
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("manifest changed between reads")
	}
	o, _, _, _ = wgFixture(t)
	count = 0
	o.InterfaceByName = func(s string) (*net.Interface, error) {
		count++
		return &net.Interface{Index: count, Name: s, Flags: net.FlagUp}, nil
	}
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("interface recreated")
	}
	o.InterfaceByName = func(s string) (*net.Interface, error) { return &net.Interface{Index: 1, Name: s}, nil }
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("down interface accepted")
	}
}
func TestWGEmptySnapshotFailureAndCancellation(t *testing.T) {
	o, m, clk, data := wgFixture(t)
	m.Peers = []WGPeer{}
	put(t, o.ManifestFile, leaseBytes(t, m, o.Clock.Now()))
	data["allowed-ips"] = ""
	data["latest-handshakes"] = ""
	out, e := o.Observe(context.Background())
	if e != nil || *out.OnlineCount != 0 {
		t.Fatal("empty snapshot rejected", e)
	}
	o.Reader = wgReadFunc(func(context.Context, string, string) ([]byte, error) { return nil, errors.New("private diagnostic") })
	if _, e = o.Observe(context.Background()); e == nil || strings.Contains(e.Error(), "private diagnostic") {
		t.Fatal("read failure invented zero or leaked", e)
	}
	o, _, clk, _ = wgFixture(t)
	orig := o.Reader
	o.Reader = wgReadFunc(func(c context.Context, i, f string) ([]byte, error) {
		clk.now = clk.now.Add(-time.Second)
		return orig.Show(c, i, f)
	})
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("clock rollback accepted")
	}
	o, _, _, _ = wgFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = o.Observe(ctx); e == nil {
		t.Fatal("cancelled result published")
	}
	if e = os.Chmod(o.ManifestFile, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = o.Observe(context.Background()); e == nil {
		t.Fatal("public manifest file accepted")
	}
}
func TestWGRuntimeStoreLockBindingAndInvalidation(t *testing.T) {
	o, _, clk, _ := wgFixture(t)
	status, e := o.Observe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "runtime")
	if e = InitRuntimeStore(dir, o.NodeID); e != nil {
		t.Fatal(e)
	}
	if e = InitRuntimeStore(dir, o.NodeID); e == nil {
		t.Fatal("existing runtime reset")
	}
	store, e := OpenRuntimeStore(dir, o.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if _, e = OpenRuntimeStore(dir, o.NodeID); e == nil {
		t.Fatal("double writer")
	}
	if e = store.Publish(status, clk.Now()); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "runtime.json")
	read, e := readRuntime(path, o.NodeID, clk.Now())
	if e != nil || read.SnapshotChecksum != status.SnapshotChecksum {
		t.Fatal("round trip", e)
	}
	if _, e = readRuntime(path, o.NodeID, clk.Now().Add(10*time.Second+time.Microsecond)); e == nil {
		t.Fatal("stale observer accepted")
	}
	status.ReadyProtocols = []string{"MOCK"}
	if e = store.Publish(status, clk.Now()); e == nil {
		t.Fatal("invalid readiness published")
	}
	if e = store.Clear(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("failed observation still present")
	}
	store.Close()
	if e = store.Publish(status, clk.Now()); e == nil {
		t.Fatal("closed publisher accepted")
	}
	if _, e = OpenRuntimeStore(dir, "7"); e == nil {
		t.Fatal("wrong node inherited state")
	}
	put(t, filepath.Join(dir, "identity.json"), []byte(`{}`))
	if _, e = OpenRuntimeStore(dir, o.NodeID); e == nil {
		t.Fatal("corrupt identity accepted")
	}
}
func TestWGRuntimeFeedsActualLinuxSampler(t *testing.T) {
	o, _, _, _ := wgFixture(t)
	o.Clock = clock.Real{}
	// No peer traffic is invented: an empty, verified peer set produces zero.
	m := wgManifest()
	m.Peers = []WGPeer{}
	put(t, o.ManifestFile, leaseBytes(t, m, o.Clock.Now()))
	o.Reader = wgReadFunc(func(_ context.Context, _ string, f string) ([]byte, error) {
		switch f {
		case "public-key":
			return []byte(m.PublicKey + "\n"), nil
		case "listen-port":
			return []byte("51820\n"), nil
		default:
			return []byte{}, nil
		}
	})
	status, e := o.Observe(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "runtime")
	if e = InitRuntimeStore(dir, m.NodeID); e != nil {
		t.Fatal(e)
	}
	store, e := OpenRuntimeStore(dir, m.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.Publish(status, o.Clock.Now()); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	for _, d := range []string{"net", "sys/kernel"} {
		if e = os.MkdirAll(filepath.Join(root, d), 0700); e != nil {
			t.Fatal(e)
		}
	}
	put(t, filepath.Join(root, "stat"), []byte("cpu 10 0 0 90 0 0 0 0 0 0\n"))
	put(t, filepath.Join(root, "net/dev"), []byte("eth0: 100 0 0 0 0 0 0 0 100 0 0 0 0 0 0 0\n"))
	put(t, filepath.Join(root, "meminfo"), []byte("MemTotal: 1000 kB\nMemAvailable: 500 kB\n"))
	put(t, filepath.Join(root, "sys/kernel/osrelease"), []byte("6.8.0-test\n"))
	done := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		e := atomicPrivate(root, "stat", []byte("cpu 20 0 0 100 0 0 0 0 0 0\n"))
		done <- e
	}()
	sampler := &LinuxSampler{Clock: o.Clock, NodeID: m.NodeID, Interface: "eth0", RuntimeFile: filepath.Join(dir, "runtime.json"), ProcRoot: root}
	h, e := sampler.Sample(context.Background())
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e != nil || h.CPU != 50 || h.Memory != 50 || h.ConfigVersion != "17" || h.SnapshotChecksum != status.SnapshotChecksum || strings.Join(h.ReadyProtocols, ",") != "WIREGUARD" {
		t.Fatal(h, e)
	}
}

func leaseBytes(t *testing.T, m any, now time.Time) []byte {
	raw := encode(t, m)
	var manifest WGManifest
	if e := json.Unmarshal(raw, &manifest); e != nil {
		t.Fatal(e)
	}
	_, digest, e := parseWGManifest(raw, manifest.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	return encode(t, nodewire.WGLease{Schema: 1, RequestNonce: strings.Repeat("a", 32), IssuedAt: now.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ExpiresAt: now.Add(30 * time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), Checksum: digest, Manifest: manifest})
}
