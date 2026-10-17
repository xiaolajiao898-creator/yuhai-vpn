package nodeagent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 第 18 轮新增的同步客户端与 WGSyncStore 此前没有测试。本文件覆盖 HANDOFF 第 3 条要求的
// 四个方面：身份与节点绑定、文件权限/锁/原子写、失败清理、并发安全，以及取快照客户端的
// 拒绝面（证书时效、响应形状、nonce 绑定、租约时效）。

const syncNodeID = "9007199254740993"
const syncOrigin = "https://node.invalid"

func syncNow() time.Time { return time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC) }

func syncLease(t *testing.T, now time.Time) nodewire.WGLease {
	t.Helper()
	var l nodewire.WGLease
	if e := json.Unmarshal(leaseBytes(t, wgManifest(), now), &l); e != nil {
		t.Fatal(e)
	}
	return l
}

// newSyncDir 返回一个尚不存在的目录路径，供 InitWGSyncStore 创建。
func newSyncDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sync")
}

func openSync(t *testing.T) (*WGSyncStore, string) {
	t.Helper()
	dir := newSyncDir(t)
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e != nil {
		t.Fatal(e)
	}
	s, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s, dir
}

// tempLeftovers 统计原子写留下的临时文件；正常路径下必须为 0。
func tempLeftovers(t *testing.T, dir string) int {
	t.Helper()
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sync-") {
			n++
		}
	}
	return n
}

func perm(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	return st.Mode().Perm()
}

// ------------------------------------------------------------------ 初始化与身份

func TestWGSyncStoreInitRejectsInvalidIdentity(t *testing.T) {
	cases := []struct {
		name   string
		dir    string
		nodeID string
		origin string
	}{
		{"相对路径", "relative/sync", syncNodeID, syncOrigin},
		{"节点 ID 为零", newSyncDir(t), "0", syncOrigin},
		{"节点 ID 非整数", newSyncDir(t), "abc", syncOrigin},
		{"节点 ID 前导零", newSyncDir(t), "007", syncOrigin},
		{"非 HTTPS 源", newSyncDir(t), syncNodeID, "http://node.invalid"},
		{"源带路径", newSyncDir(t), syncNodeID, "https://node.invalid/x"},
		{"源带查询串", newSyncDir(t), syncNodeID, "https://node.invalid?a=1"},
		{"源带凭据", newSyncDir(t), syncNodeID, "https://u:p@node.invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if e := InitWGSyncStore(c.dir, c.nodeID, c.origin); e == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestWGSyncStoreInitRequiresNewPrivateDirectory(t *testing.T) {
	dir := newSyncDir(t)
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e != nil {
		t.Fatal(e)
	}
	if got := perm(t, dir); got != 0700 {
		t.Fatalf("状态目录权限应为 0700，实际 %o", got)
	}
	if got := perm(t, filepath.Join(dir, "identity.json")); got != 0600 {
		t.Fatalf("identity.json 权限应为 0600，实际 %o", got)
	}
	// 不得在已存在的目录上重复初始化，避免覆盖别的节点的状态。
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e == nil {
		t.Fatal("对已存在目录的重复初始化应当被拒绝")
	}
}

func TestWGSyncStoreOpenRequiresMatchingIdentity(t *testing.T) {
	dir := newSyncDir(t)
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name   string
		nodeID string
		origin string
	}{
		{"节点 ID 不匹配", "7", syncOrigin},
		{"控制面地址不匹配", syncNodeID, "https://other.invalid"},
		{"端口不同即视为不同控制面", syncNodeID, "https://node.invalid:8443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e := OpenWGSyncStore(dir, c.nodeID, c.origin)
			if e == nil {
				s.Close()
				t.Fatal("身份不匹配时应当拒绝打开")
			}
		})
	}
	// 身份一致时可以打开，并且上一次失败没有留下锁。
	s, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e != nil {
		t.Fatalf("身份一致却打不开: %v", e)
	}
	s.Close()
}

func TestWGSyncStoreOpenRejectsTamperedIdentity(t *testing.T) {
	dir := newSyncDir(t)
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "identity.json")
	for _, raw := range []string{
		`{"schema":1,"node_id":"9007199254740993","endpoint":"https://node.invalid/node/v1/heartbeat","extra":1}`,
		`{"schema":1,"node_id":"9007199254740993"}`,
		`{"schema":2,"node_id":"9007199254740993","endpoint":"https://node.invalid/node/v1/heartbeat"}`,
		`not json`,
	} {
		put(t, path, []byte(raw))
		s, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
		if e == nil {
			s.Close()
			t.Fatalf("被篡改的 identity.json 仍被接受: %s", raw)
		}
	}
}

// ------------------------------------------------------------------ 锁与并发

func TestWGSyncStoreLockIsExclusive(t *testing.T) {
	first, dir := openSync(t)

	second, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e == nil {
		second.Close()
		t.Fatal("同一状态目录被两个进程同时持有")
	}

	// 释放后另一个持有者才能接管。
	first.Close()
	third, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e != nil {
		t.Fatalf("释放锁后仍无法打开: %v", e)
	}
	third.Close()

	// Close 可重复调用。
	first.Close()
}

func TestWGSyncStoreRejectsUseAfterClose(t *testing.T) {
	s, _ := openSync(t)
	s.Close()
	if e := s.Publish(syncLease(t, syncNow()), syncNow()); e == nil {
		t.Fatal("关闭后仍允许发布快照")
	}
	if e := s.Clear(); e == nil {
		t.Fatal("关闭后仍允许清除快照")
	}
}

func TestWGSyncStoreConcurrentPublishAndClear(t *testing.T) {
	s, dir := openSync(t)
	now := syncNow()
	lease := syncLease(t, now)

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if e := s.Publish(lease, now); e != nil {
				errs <- e
			}
		}()
		go func() {
			defer wg.Done()
			if e := s.Clear(); e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("并发读写出错: %v", e)
	}
	if n := tempLeftovers(t, dir); n != 0 {
		t.Fatalf("并发后残留 %d 个临时文件", n)
	}
}

// ------------------------------------------------------------ 原子写与失败清理

func TestWGSyncStorePublishIsAtomicAndPrivate(t *testing.T) {
	s, dir := openSync(t)
	now := syncNow()
	if e := s.Publish(syncLease(t, now), now); e != nil {
		t.Fatal(e)
	}
	snapshot := filepath.Join(dir, "manifest.json")
	if got := perm(t, snapshot); got != 0600 {
		t.Fatalf("manifest.json 权限应为 0600，实际 %o", got)
	}
	if n := tempLeftovers(t, dir); n != 0 {
		t.Fatalf("发布后残留 %d 个临时文件", n)
	}
	raw, e := os.ReadFile(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := nodewire.ParseWGLease(raw, syncNodeID, now); e != nil {
		t.Fatalf("落盘内容不是有效租约: %v", e)
	}
}

func TestWGSyncStorePublishRejectsInvalidLeaseAndKeepsPrevious(t *testing.T) {
	s, dir := openSync(t)
	now := syncNow()
	good := syncLease(t, now)
	if e := s.Publish(good, now); e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}

	cases := []struct {
		name   string
		lease  nodewire.WGLease
		at     time.Time
		expect string
	}{
		{"租约已过期", good, now.Add(31 * time.Second), "过期租约"},
		{"时钟早于签发时间", good, now.Add(-time.Minute), "未生效租约"},
		{"nonce 非法", func() nodewire.WGLease { l := good; l.RequestNonce = "zz"; return l }(), now, "非法 nonce"},
		{"校验和不匹配", func() nodewire.WGLease { l := good; l.Checksum = strings.Repeat("0", 64); return l }(), now, "错误校验和"},
		{"节点 ID 不匹配", func() nodewire.WGLease {
			l := good
			m := l.Manifest
			m.NodeID = "7"
			l.Manifest = m
			return l
		}(), now, "他节点清单"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if e := s.Publish(c.lease, c.at); e == nil {
				t.Fatalf("%s 被接受", c.expect)
			}
			after, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if e != nil {
				t.Fatalf("被拒绝的发布破坏了已有快照: %v", e)
			}
			if string(after) != string(before) {
				t.Fatal("被拒绝的发布改写了已有快照")
			}
			if n := tempLeftovers(t, dir); n != 0 {
				t.Fatalf("失败路径残留 %d 个临时文件", n)
			}
		})
	}
}

func TestWGSyncStoreClearIsIdempotentAndOpenInvalidatesStaleSnapshot(t *testing.T) {
	dir := newSyncDir(t)
	if e := InitWGSyncStore(dir, syncNodeID, syncOrigin); e != nil {
		t.Fatal(e)
	}
	s, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e != nil {
		t.Fatal(e)
	}
	now := syncNow()
	if e := s.Publish(syncLease(t, now), now); e != nil {
		t.Fatal(e)
	}
	if e := s.Clear(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(e) {
		t.Fatal("Clear 之后快照仍然存在")
	}
	// 快照不存在时再次清除不应报错。
	if e := s.Clear(); e != nil {
		t.Fatalf("重复 Clear 报错: %v", e)
	}

	// 重新发布后不释放锁直接重开，应当在打开时失效旧快照。
	if e := s.Publish(syncLease(t, now), now); e != nil {
		t.Fatal(e)
	}
	s.Close()
	reopened, e := OpenWGSyncStore(dir, syncNodeID, syncOrigin)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if _, e := os.Stat(filepath.Join(dir, "manifest.json")); !os.IsNotExist(e) {
		t.Fatal("重新打开后陈旧快照未被失效——重启后可能沿用过期授权")
	}
}

// ------------------------------------------------------------ 取快照客户端

// syncServer 启动一个 TLS 测试服务端，并返回绑定到它的 Client。
func syncServer(t *testing.T, clk *fixedClock, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		endpoint: srv.URL + "/heartbeat",
		nodeID:   syncNodeID,
		client:   srv.Client(),
		cert:     &x509.Certificate{NotBefore: clk.now.Add(-time.Hour), NotAfter: clk.now.Add(time.Hour)},
		clock:    clk,
	}
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, r *http.Request, mutate func(*nodewire.WGLease)) {
	t.Helper()
	var l nodewire.WGLease
	if e := json.Unmarshal(leaseBytes(t, wgManifest(), syncNow()), &l); e != nil {
		t.Fatal(e)
	}
	l.RequestNonce = r.Header.Get("X-Snapshot-Nonce")
	if mutate != nil {
		mutate(&l)
	}
	body, e := json.Marshal(map[string]any{"data": l, "request_id": "req-1"})
	if e != nil {
		t.Fatal(e)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func TestFetchWireGuardURLDerivedFromHeartbeatEndpoint(t *testing.T) {
	c := &Client{endpoint: "https://node.invalid/node/v1/heartbeat"}
	if got := c.WireGuardURL(); got != "https://node.invalid/node/v1/wireguard/manifest" {
		t.Fatalf("清单地址错误: %s", got)
	}
}

func TestFetchWireGuardHappyPathBindsNonce(t *testing.T) {
	clk := &fixedClock{now: syncNow()}
	var seen string
	c := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wireguard/manifest" {
			t.Errorf("请求路径错误: %s", r.URL.Path)
		}
		seen = r.Header.Get("X-Snapshot-Nonce")
		writeEnvelope(t, w, r, nil)
	})
	l, e := c.FetchWireGuard(context.Background())
	if e != nil {
		t.Fatalf("合法响应被拒绝: %v", e)
	}
	if !nodewire.ValidWGNonce(seen) {
		t.Fatalf("客户端发出的 nonce 不合规: %q", seen)
	}
	if l.RequestNonce != seen {
		t.Fatal("返回的租约未绑定本次请求的 nonce")
	}
	if l.Manifest.NodeID != syncNodeID {
		t.Fatal("返回的清单不属于本节点")
	}
}

func TestFetchWireGuardNonceIsFreshEachCall(t *testing.T) {
	clk := &fixedClock{now: syncNow()}
	seen := map[string]bool{}
	var mu sync.Mutex
	c := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("X-Snapshot-Nonce")] = true
		mu.Unlock()
		writeEnvelope(t, w, r, nil)
	})
	for i := 0; i < 5; i++ {
		if _, e := c.FetchWireGuard(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("5 次请求只出现 %d 个不同 nonce——存在重放风险", len(seen))
	}
}

func TestFetchWireGuardRejectsExpiredCertificate(t *testing.T) {
	clk := &fixedClock{now: syncNow()}
	c := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) {
		t.Error("证书失效时不应发出请求")
	})
	for _, at := range []time.Time{clk.now.Add(-2 * time.Hour), clk.now.Add(2 * time.Hour)} {
		clk.now = at
		_, e := c.FetchWireGuard(context.Background())
		var de *DeliveryError
		if !errors.As(e, &de) || de.Kind != "certificate_expired" {
			t.Fatalf("证书时效之外应报 certificate_expired，实际 %v", e)
		}
	}
}

func TestFetchWireGuardRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		kind    string
	}{
		{"非 200", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, "snapshot_rejected"},
		{"404", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, "snapshot_rejected"},
		{"内容类型错误", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("{}"))
		}, "invalid_snapshot"},
		{"缺少内容类型", func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = w.Write([]byte("{}"))
		}, "invalid_snapshot"},
		{"响应体超限", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(make([]byte, 1048577))
		}, "invalid_snapshot"},
		{"信封多余字段", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{},"request_id":"r","extra":1}`))
		}, "invalid_snapshot"},
		{"缺少 request_id", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{},"request_id":""}`))
		}, "invalid_snapshot"},
		{"非 JSON", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("nope"))
		}, "invalid_snapshot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &fixedClock{now: syncNow()}
			client := syncServer(t, clk, c.handler)
			_, e := client.FetchWireGuard(context.Background())
			var de *DeliveryError
			if !errors.As(e, &de) || de.Kind != c.kind {
				t.Fatalf("应报 %s，实际 %v", c.kind, e)
			}
		})
	}
}

func TestFetchWireGuardRejectsUnboundOrStaleLease(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*nodewire.WGLease)
		at     func(time.Time) time.Time
	}{
		{"nonce 与请求不符", func(l *nodewire.WGLease) { l.RequestNonce = strings.Repeat("f", 32) }, nil},
		{"租约已过期", nil, func(now time.Time) time.Time { return now.Add(31 * time.Second) }},
		{"租约尚未生效", nil, func(now time.Time) time.Time { return now.Add(-time.Minute) }},
		{"校验和不匹配", func(l *nodewire.WGLease) { l.Checksum = strings.Repeat("0", 64) }, nil},
		{"清单属于其他节点", func(l *nodewire.WGLease) {
			m := l.Manifest
			m.NodeID = "7"
			l.Manifest = m
		}, nil},
		{"schema 不为 1", func(l *nodewire.WGLease) { l.Schema = 2 }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &fixedClock{now: syncNow()}
			client := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) {
				writeEnvelope(t, w, r, c.mutate)
			})
			if c.at != nil {
				// 证书仍在有效期内，只让判定租约的时钟偏移。
				client.cert = &x509.Certificate{NotBefore: syncNow().Add(-24 * time.Hour), NotAfter: syncNow().Add(24 * time.Hour)}
				clk.now = c.at(syncNow())
			}
			_, e := client.FetchWireGuard(context.Background())
			var de *DeliveryError
			if !errors.As(e, &de) || de.Kind != "invalid_snapshot" {
				t.Fatalf("应报 invalid_snapshot，实际 %v", e)
			}
		})
	}
}

func TestFetchWireGuardReportsUnreachableControlPlane(t *testing.T) {
	clk := &fixedClock{now: syncNow()}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := &Client{
		endpoint: srv.URL + "/heartbeat",
		nodeID:   syncNodeID,
		client:   srv.Client(),
		cert:     &x509.Certificate{NotBefore: clk.now.Add(-time.Hour), NotAfter: clk.now.Add(time.Hour)},
		clock:    clk,
	}
	srv.Close() // 控制面下线
	_, e := c.FetchWireGuard(context.Background())
	var de *DeliveryError
	if !errors.As(e, &de) || de.Kind != "snapshot_unavailable" {
		t.Fatalf("控制面不可达应报 snapshot_unavailable，实际 %v", e)
	}
}

func TestFetchWireGuardHonoursContextCancellation(t *testing.T) {
	clk := &fixedClock{now: syncNow()}
	release := make(chan struct{})
	c := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, e := c.FetchWireGuard(ctx)
	close(release)
	var de *DeliveryError
	if !errors.As(e, &de) || de.Kind != "snapshot_unavailable" {
		t.Fatalf("取消后应报 snapshot_unavailable，实际 %v", e)
	}
}

func TestFetchWireGuardStatusCodeIsReported(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 429, 500, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			clk := &fixedClock{now: syncNow()}
			c := syncServer(t, clk, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
			_, e := c.FetchWireGuard(context.Background())
			var de *DeliveryError
			if !errors.As(e, &de) || de.Code != code {
				t.Fatalf("未回报状态码 %d，实际 %v", code, e)
			}
		})
	}
}
