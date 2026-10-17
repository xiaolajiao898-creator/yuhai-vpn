package account

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 隧道地址是全局唯一的，而本包的实库测试共用 public schema，所以每个用例都在
// 自己随机的一段私有网段里分配，彼此不会抢地址，也不会被上一次运行的残留影响。
func randomPool(t *testing.T, ones int) *net.IPNet {
	t.Helper()
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	host := byte(0)
	if ones > 24 {
		host = b[2] & byte(0xFF<<uint(32-ones))
	}
	_, pool, err := net.ParseCIDR(fmt.Sprintf("10.%d.%d.%d/%d", b[0], b[1], host, ones))
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func newWGKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

type wgFixture struct {
	svc    *Service
	ctx    context.Context
	device int64
}

func newWGFixture(t *testing.T, pool *net.IPNet, reserved int) wgFixture {
	t.Helper()
	svc, ctx, _, _, registered := newAccountIntegrationFixture(t)
	svc.WGPool, svc.WGReserved = pool, reserved
	id, err := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return wgFixture{svc: svc, ctx: ctx, device: id}
}

// credential 读回凭据当前状态；wg_pubkey 与 wg_address 必须同时为空或同时有值，
// 否则控制面清单会整体拒绝下发，连累该节点上的所有用户。
func (f wgFixture) credential(t *testing.T) (pubkey, address string, rotated *time.Time) {
	t.Helper()
	var k, a *string
	err := f.svc.Repo.Pool.QueryRow(f.ctx, `SELECT wg_pubkey,wg_address::text,rotated_at
 FROM device_credentials WHERE device_id=$1`, f.device).Scan(&k, &a, &rotated)
	if err != nil {
		t.Fatal(err)
	}
	if (k == nil) != (a == nil) {
		t.Fatalf("公钥与地址必须同时写入，实际 pubkey=%v address=%v", k, a)
	}
	if k == nil {
		return "", "", rotated
	}
	return *k, *a, rotated
}

func wgCode(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var coded *httpx.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("期望错误码 %d，实际 %v", want, err)
	}
}

func TestWGKeyAllocatesAddressAndIsIdempotentIntegration(t *testing.T) {
	pool := randomPool(t, 24)
	f := newWGFixture(t, pool, 16)
	key := newWGKey(t)

	if err := f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: key}); err != nil {
		t.Fatal(err)
	}
	gotKey, address, firstRotated := f.credential(t)
	if gotKey != key {
		t.Fatalf("公钥未保存：%q", gotKey)
	}
	// 第一个分配出去的地址应当正好是保留段之后的那一个。
	want := pool.IP.To4()
	want[3] += 16
	if address != want.String()+"/32" {
		t.Fatalf("地址不符，期望 %s/32，实际 %s", want, address)
	}
	if firstRotated == nil {
		t.Fatal("首次上传应记录 rotated_at")
	}

	// 重复上传同一把公钥：既不换地址，也不该看起来像一次轮换。
	time.Sleep(2 * time.Millisecond)
	if err := f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: key}); err != nil {
		t.Fatal(err)
	}
	againKey, againAddress, againRotated := f.credential(t)
	if againKey != key || againAddress != address {
		t.Fatalf("重复上传改动了凭据：%s %s", againKey, againAddress)
	}
	if againRotated == nil || !againRotated.Equal(*firstRotated) {
		t.Fatalf("重复上传不应刷新 rotated_at：%v → %v", firstRotated, againRotated)
	}
}

func TestWGKeyRotationKeepsAddressIntegration(t *testing.T) {
	f := newWGFixture(t, randomPool(t, 24), 16)
	first, second := newWGKey(t), newWGKey(t)
	if err := f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: first}); err != nil {
		t.Fatal(err)
	}
	_, address, firstRotated := f.credential(t)

	time.Sleep(2 * time.Millisecond)
	if err := f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: second}); err != nil {
		t.Fatal(err)
	}
	key, again, rotated := f.credential(t)
	if key != second {
		t.Fatalf("轮换后公钥未更新：%q", key)
	}
	if again != address {
		t.Fatalf("轮换不应更换隧道地址：%s → %s", address, again)
	}
	if rotated == nil || !rotated.After(*firstRotated) {
		t.Fatalf("轮换应刷新 rotated_at：%v → %v", firstRotated, rotated)
	}
	// 旧公钥必须彻底消失，否则节点上会同时留着两把钥匙。
	var stillThere int
	if err := f.svc.Repo.Pool.QueryRow(f.ctx, `SELECT count(*) FROM device_credentials WHERE wg_pubkey=$1`, first).
		Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 0 {
		t.Fatal("轮换后旧公钥仍在库里")
	}
}

func TestWGKeyRejectsKeyOwnedByAnotherDeviceIntegration(t *testing.T) {
	pool := randomPool(t, 24)
	owner := newWGFixture(t, pool, 16)
	other := newWGFixture(t, pool, 16)
	key := newWGKey(t)
	if err := owner.svc.SetWireGuardKey(owner.ctx, owner.device, WGKeyInput{PubKey: key}); err != nil {
		t.Fatal(err)
	}
	ownerKey, ownerAddress, _ := owner.credential(t)

	// 抢占别人的公钥必须失败，而不是把对方顶掉——顶掉意味着对方的流量会被引到这里。
	err := other.svc.SetWireGuardKey(other.ctx, other.device, WGKeyInput{PubKey: key})
	wgCode(t, err, httpx.ErrConflict)

	if k, a, _ := owner.credential(t); k != ownerKey || a != ownerAddress {
		t.Fatalf("被抢占方的凭据被改动了：%s %s", k, a)
	}
	if k, a, _ := other.credential(t); k != "" || a != "" {
		t.Fatalf("失败的上传不应留下任何痕迹：%s %s", k, a)
	}
}

func TestWGKeyRejectsInvalidKeyIntegration(t *testing.T) {
	f := newWGFixture(t, randomPool(t, 24), 16)
	zeros := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, bad := range []string{
		"", "not-base64!!", zeros,
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
		strings.TrimRight(newWGKey(t), "="), // 非规范编码
		" " + newWGKey(t),
	} {
		err := f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: bad})
		wgCode(t, err, httpx.ErrBadRequest)
	}
	if k, a, _ := f.credential(t); k != "" || a != "" {
		t.Fatalf("非法公钥不应写库：%s %s", k, a)
	}
}

func TestWGKeyRefusesRevokedDeviceIntegration(t *testing.T) {
	f := newWGFixture(t, randomPool(t, 24), 16)
	if _, err := f.svc.Repo.Pool.Exec(f.ctx,
		`UPDATE device_credentials SET status='REVOKED' WHERE device_id=$1`, f.device); err != nil {
		t.Fatal(err)
	}
	wgCode(t, f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: newWGKey(t)}), httpx.ErrDeviceRevoked)

	// 设备本身被移除同样拒绝。
	if _, err := f.svc.Repo.Pool.Exec(f.ctx,
		`UPDATE device_credentials SET status='ACTIVE' WHERE device_id=$1`, f.device); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Repo.Pool.Exec(f.ctx, `UPDATE devices SET status='REVOKED' WHERE id=$1`, f.device); err != nil {
		t.Fatal(err)
	}
	wgCode(t, f.svc.SetWireGuardKey(f.ctx, f.device, WGKeyInput{PubKey: newWGKey(t)}), httpx.ErrDeviceRevoked)

	// 不存在的设备不区分「没有」和「已移除」。
	wgCode(t, f.svc.SetWireGuardKey(f.ctx, -1, WGKeyInput{PubKey: newWGKey(t)}), httpx.ErrDeviceRevoked)
}

func TestWGKeyConcurrentAllocationsAreUniqueIntegration(t *testing.T) {
	const devices = 6
	pool := randomPool(t, 24)
	fixtures := make([]wgFixture, devices)
	for i := range fixtures {
		fixtures[i] = newWGFixture(t, pool, 16)
	}
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(devices)
	done.Add(devices)
	errs := make([]error, devices)
	for i := range fixtures {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			errs[i] = fixtures[i].svc.SetWireGuardKey(fixtures[i].ctx, fixtures[i].device, WGKeyInput{PubKey: newWGKey(t)})
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 台设备分配失败：%v", i, err)
		}
		_, address, _ := fixtures[i].credential(t)
		if seen[address] {
			t.Fatalf("地址重复分配：%s", address)
		}
		seen[address] = true
	}
	if len(seen) != devices {
		t.Fatalf("期望 %d 个不同地址，实际 %d 个", devices, len(seen))
	}
}

func TestWGKeyExhaustedPoolIntegration(t *testing.T) {
	// /28 共 16 个地址，保留 14 个、末位是广播地址，因此只剩一个可分配。
	pool := randomPool(t, 28)
	first := newWGFixture(t, pool, 14)
	second := newWGFixture(t, pool, 14)
	if err := first.svc.SetWireGuardKey(first.ctx, first.device, WGKeyInput{PubKey: newWGKey(t)}); err != nil {
		t.Fatal(err)
	}
	wgCode(t, second.svc.SetWireGuardKey(second.ctx, second.device, WGKeyInput{PubKey: newWGKey(t)}),
		httpx.ErrWGAddressExhausted)
	if k, a, _ := second.credential(t); k != "" || a != "" {
		t.Fatalf("地址池耗尽时不应留下半截记录：%s %s", k, a)
	}
	// 池子满了不影响已经拿到地址的设备轮换公钥。
	if err := first.svc.SetWireGuardKey(first.ctx, first.device, WGKeyInput{PubKey: newWGKey(t)}); err != nil {
		t.Fatalf("已分配地址的设备轮换不应受地址池影响：%v", err)
	}
}

func TestWGKeyNeverReusesAddressOfRemovedDeviceIntegration(t *testing.T) {
	pool := randomPool(t, 24)
	gone := newWGFixture(t, pool, 16)
	if err := gone.svc.SetWireGuardKey(gone.ctx, gone.device, WGKeyInput{PubKey: newWGKey(t)}); err != nil {
		t.Fatal(err)
	}
	_, freed, _ := gone.credential(t)
	if _, err := gone.svc.Repo.Pool.Exec(gone.ctx,
		`UPDATE devices SET status='REVOKED' WHERE id=$1`, gone.device); err != nil {
		t.Fatal(err)
	}
	if _, err := gone.svc.Repo.Pool.Exec(gone.ctx,
		`UPDATE device_credentials SET status='REVOKED' WHERE device_id=$1`, gone.device); err != nil {
		t.Fatal(err)
	}

	fresh := newWGFixture(t, pool, 16)
	if err := fresh.svc.SetWireGuardKey(fresh.ctx, fresh.device, WGKeyInput{PubKey: newWGKey(t)}); err != nil {
		t.Fatal(err)
	}
	_, got, _ := fresh.credential(t)
	if got == freed {
		t.Fatalf("新设备拿到了已移除设备的地址 %s——节点上若还留着旧 peer，流量会走错对端", got)
	}
}

func TestWGKeyHandlerIntegration(t *testing.T) {
	f := newWGFixture(t, randomPool(t, 24), 16)
	h := &Handler{Svc: f.svc}
	r := gin.New()
	r.PUT("/devices/current/wg-key", func(c *gin.Context) { httpx.SetPrincipal(c, 0, f.device) }, h.putWGKey)

	do := func(body, idem string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/devices/current/wg-key", strings.NewReader(body)).WithContext(f.ctx)
		req.Header.Set("Idempotency-Key", idem)
		r.ServeHTTP(w, req)
		return w
	}

	w := do(`{"wg_pubkey":"`+newWGKey(t)+`"}`, uuid.NewString())
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("意外响应：%d %s %q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
	if w := do(`{"wg_pubkey":"`+newWGKey(t)+`"}`, "not-a-uuid"); w.Code != http.StatusBadRequest {
		t.Fatalf("非法幂等键应被拒绝：%d", w.Code)
	}
	if w := do(`{`, uuid.NewString()); w.Code != http.StatusBadRequest {
		t.Fatalf("非法请求体应被拒绝：%d", w.Code)
	}
}
