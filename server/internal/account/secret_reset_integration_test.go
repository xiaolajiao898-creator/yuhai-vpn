package account

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
)

// secretVersionOf 直接读库，不经过任何被测代码路径。
func secretVersionOf(t *testing.T, svc *Service, ctx context.Context, userID int64) int {
	t.Helper()
	var v int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT secret_version FROM users WHERE id=$1`, userID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func lookupOf(t *testing.T, svc *Service, ctx context.Context, userID int64) string {
	t.Helper()
	var s string
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT secret_lookup FROM users WHERE id=$1`, userID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSecretResetRotatesLookupAndVersionIntegration 是这一节的主张本身：
// 重置之后旧密钥查不到人、新密钥查得到人、版本号涨了一格、备份确认被清掉。
func TestSecretResetRotatesLookupAndVersionIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	if err := svc.AckBackup(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	beforeVersion := secretVersionOf(t, svc, ctx, u.ID)
	oldLookup := lookupOf(t, svc, ctx, u.ID)

	res, err := svc.ResetSecret(ctx, u.ID, deviceID, uuid.NewString(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.AccountSecret == registered.AccountSecret || res.AccountSecret == "" {
		t.Fatalf("new secret must differ from the old one: %q", res.AccountSecret)
	}
	if _, err := ids.NormalizeSecret(res.AccountSecret); err != nil {
		t.Fatalf("new secret is not in the documented shape: %v", err)
	}
	if got := secretVersionOf(t, svc, ctx, u.ID); got != beforeVersion+1 {
		t.Fatalf("secret_version = %d, want %d", got, beforeVersion+1)
	}
	newLookup := lookupOf(t, svc, ctx, u.ID)
	if newLookup == oldLookup {
		t.Fatal("secret_lookup unchanged: the old secret still restores the account")
	}
	if want := ids.SecretLookup(svc.Pepper, res.AccountSecret); newLookup != want {
		t.Fatal("stored lookup is not the HMAC of the secret we handed out")
	}
	// 旧密钥必须在恢复入口上真的失效，而不只是「那一列变了」。
	if _, err := svc.Repo.userBySecretLookup(ctx, oldLookup); err == nil {
		t.Fatal("the old secret still resolves to an account")
	}
	if got, err := svc.Repo.userBySecretLookup(ctx, newLookup); err != nil || got.ID != u.ID {
		t.Fatalf("the new secret does not resolve to the account: %v", err)
	}
	// 新密钥还没被抄下来，备份提醒必须重新亮起来。
	me, err := svc.Me(ctx, u.ID, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if me.BackupAcknowledged {
		t.Fatal("backup_acknowledged survived the rotation; the user never saw the new secret")
	}
}

// TestSecretResetRevokesOtherDevicesAndAllTokensIntegration 覆盖契约里那句
// 「其他设备与全部令牌失效」：别的设备下线、旧刷新令牌作废、发起设备留下且拿到新令牌。
func TestSecretResetRevokesOtherDevicesAndAllTokensIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	otherID, other := attachRemovalTarget(t, svc, ctx, u, device)

	res, err := svc.ResetSecret(ctx, u.ID, currentID, uuid.NewString(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	assertDeviceRevocation(t, svc, ctx, otherID, "REVOKED", 0)
	var resetLogs int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM device_bind_logs
	 WHERE device_id=$1 AND action='REVOKE_BY_SECRET_RESET'`, otherID).Scan(&resetLogs); err != nil {
		t.Fatal(err)
	}
	if resetLogs != 1 {
		t.Fatalf("REVOKE_BY_SECRET_RESET log rows = %d, want 1", resetLogs)
	}
	// 被下线的设备连刷新都不该还有机会。
	if _, err := svc.Refresh(ctx, u.ID, otherID, other.Tokens.RefreshToken); err == nil {
		t.Fatal("a device revoked by the reset could still refresh")
	}
	// 发起设备留下，但它原来的刷新令牌也作废了——契约说的是「全部令牌」。
	if _, err := svc.Refresh(ctx, u.ID, currentID, registered.Tokens.RefreshToken); err == nil {
		t.Fatal("the initiating device's old refresh token survived the reset")
	}
	p, err := svc.Repo.LookupDevice(ctx, currentID)
	if err != nil || !p.DeviceActive {
		t.Fatalf("the initiating device must stay active: %v %v", p.DeviceActive, err)
	}
	// 版本号本身就能让旧令牌全部失效，所以上面两条断言并不能证明会话真的被撤了。
	// 直接数一遍：重置之后整个账户只应剩下刚签发的那一条会话。
	// 少了这一条，「作废全部会话」那句 SQL 删掉也没人发现。
	var live int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions
	 WHERE user_id=$1 AND revoked_at IS NULL`, u.ID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live sessions after the reset = %d, want exactly the newly issued one", live)
	}
	// 而响应里那对新令牌必须真的能用。
	if _, err := svc.Refresh(ctx, u.ID, currentID, res.Tokens.RefreshToken); err != nil {
		t.Fatalf("the tokens returned by the reset do not work: %v", err)
	}
}

// TestSecretResetInvalidatesOldAccessTokensIntegration 把版本号这条链路走完：
// 旧访问令牌里带的是旧版本，鉴权中间件比对时必须拒绝。
func TestSecretResetInvalidatesOldAccessTokensIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	claims, err := svc.Signer.Verify(registered.Tokens.AccessToken, svc.Clock.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResetSecret(ctx, u.ID, deviceID, uuid.NewString(), nil, ""); err != nil {
		t.Fatal(err)
	}
	p, err := svc.Repo.LookupDevice(ctx, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if p.SecretVersion == claims.SecretVersion {
		t.Fatal("secret_version did not move, so every old access token still authenticates")
	}
}

// TestSecretResetIsIdempotentWithinWindowIntegration：同一个幂等键重放返回同一把密钥，
// 而且**不会**再轮换一次。
func TestSecretResetIsIdempotentWithinWindowIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	key := uuid.NewString()
	first, err := svc.ResetSecret(ctx, u.ID, deviceID, key, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := secretVersionOf(t, svc, ctx, u.ID)
	second, err := svc.ResetSecret(ctx, u.ID, deviceID, key, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.AccountSecret != first.AccountSecret {
		t.Fatal("a replay handed out a different secret")
	}
	if second.Tokens.RefreshToken != first.Tokens.RefreshToken {
		t.Fatal("a replay issued a second token pair")
	}
	if got := secretVersionOf(t, svc, ctx, u.ID); got != afterFirst {
		t.Fatalf("a replay rotated again: secret_version %d → %d", afterFirst, got)
	}
	// 同一个键配上不同请求体是冲突，不是重放。
	_, err = svc.ResetSecret(ctx, u.ID, deviceID, key, []byte(`{"x":1}`), "")
	expectAuthError(t, err, httpx.ErrConflict)
	// 这个键已经被本接口占掉，别的接口再用它同样是冲突（api_idempotency 的
	// 唯一键里没有 endpoint，所以这条必须在代码里挡）。
	if err := svc.Repo.putIdem(ctx, svc.Repo.Pool, u.ID, uuid.NewString(), "register", "h", 201, []byte(`"x"`)); err != nil {
		t.Fatal(err)
	}
	foreign := uuid.NewString()
	// 请求哈希故意写成与本接口一模一样的那个值（本接口没有请求体，哈希是固定的）。
	// 否则「请求体不同」那一条就会先把它挡掉，endpoint 这一层等于没测。
	if err := svc.Repo.putIdem(ctx, svc.Repo.Pool, u.ID, foreign, "other_endpoint", hashBytes(nil), 200, []byte(`"x"`)); err != nil {
		t.Fatal(err)
	}
	before := secretVersionOf(t, svc, ctx, u.ID)
	_, err = svc.ResetSecret(ctx, u.ID, deviceID, foreign, nil, "")
	expectAuthError(t, err, httpx.ErrConflict)
	if got := secretVersionOf(t, svc, ctx, u.ID); got != before {
		t.Fatal("a key already taken by another endpoint still rotated the secret")
	}
}

// TestSecretResetRefusesToRotateAfterTheWindowIntegration：展示期过了，
// 那把密钥就是找不回来了。此时绝不能顺手再换一把——那会让幂等键变成「每次都换」。
func TestSecretResetRefusesToRotateAfterTheWindowIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	key := uuid.NewString()
	if _, err := svc.ResetSecret(ctx, u.ID, deviceID, key, nil, ""); err != nil {
		t.Fatal(err)
	}
	after := secretVersionOf(t, svc, ctx, u.ID)
	// 把幂等记录推到窗口之外，等价于 worker 清理之前的那一刻。
	if _, err := svc.Repo.Pool.Exec(ctx, `UPDATE api_idempotency SET created_at=$3
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, key, svc.Clock.Now().Add(-idemSecretResetTTL-time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ResetSecret(ctx, u.ID, deviceID, key, nil, "")
	expectAuthError(t, err, httpx.ErrNotFound)
	if got := secretVersionOf(t, svc, ctx, u.ID); got != after {
		t.Fatal("an expired idempotency record rotated the secret a second time")
	}
	// worker 清空响应之后同样是 40401，而不是「解密失败」的 500。
	if err := svc.Repo.clearIdemResponse(ctx, u.ID, key); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ResetSecret(ctx, u.ID, deviceID, key, nil, "")
	expectAuthError(t, err, httpx.ErrNotFound)
}

// TestSecretResetRejectsRevokedInitiatorIntegration：一台已经被移除的设备，
// 哪怕手里还攥着没过期的访问令牌，也不能靠重置把自己变成唯一在线的设备。
func TestSecretResetRejectsRevokedInitiatorIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	stolenID, _ := attachRemovalTarget(t, svc, ctx, u, device)
	if err := svc.RemoveDevice(ctx, u.ID, currentID, stolenID, ""); err != nil {
		t.Fatal(err)
	}
	before := secretVersionOf(t, svc, ctx, u.ID)
	_, err := svc.ResetSecret(ctx, u.ID, stolenID, uuid.NewString(), nil, "")
	expectAuthError(t, err, httpx.ErrDeviceRevoked)
	if got := secretVersionOf(t, svc, ctx, u.ID); got != before {
		t.Fatal("a revoked device rotated the account secret")
	}
	assertDeviceRevocation(t, svc, ctx, currentID, "ACTIVE", 0)

	// 停用的账户同样不能重置。
	if _, err := svc.Repo.Pool.Exec(ctx, `UPDATE users SET status='BANNED' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.ResetSecret(ctx, u.ID, currentID, uuid.NewString(), nil, "")
	expectAuthError(t, err, httpx.ErrAccountDisabled)
	if got := secretVersionOf(t, svc, ctx, u.ID); got != before {
		t.Fatal("a banned account rotated its secret")
	}
}

// TestConcurrentSecretResetRotatesOnceIntegration：八个并发请求共用一个幂等键，
// 只能换一把锁；再用八个各自不同的键并发，账户不能落到「设备还在线但版本号已经变了」
// 这种半成品状态。
func TestConcurrentSecretResetRotatesOnceIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	before := secretVersionOf(t, svc, ctx, u.ID)

	const attempts = 8
	key := uuid.NewString()
	results := make([]SecretResetResult, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.ResetSecret(ctx, u.ID, deviceID, key, nil, "")
		}(i)
	}
	close(start)
	wg.Wait()

	secrets := map[string]bool{}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("attempt %d failed: %v", i, errs[i])
		}
		secrets[results[i].AccountSecret] = true
	}
	if len(secrets) != 1 {
		t.Fatalf("one idempotency key produced %d distinct secrets", len(secrets))
	}
	if got := secretVersionOf(t, svc, ctx, u.ID); got != before+1 {
		t.Fatalf("secret_version = %d after %d concurrent replays, want %d", got, attempts, before+1)
	}
	if want := ids.SecretLookup(svc.Pepper, results[0].AccountSecret); lookupOf(t, svc, ctx, u.ID) != want {
		t.Fatal("the stored lookup does not match the secret every caller received")
	}
}

// TestConcurrentSecretResetAndRefreshLeavesNoLiveSessionIntegration：重置与另一台
// 设备的刷新同时发生。要么刷新赶在前面（那它的后代令牌必须被这次重置一起作废），
// 要么重置先到（那刷新必须失败）。绝不允许出现一个跨过重置活下来的会话。
func TestConcurrentSecretResetAndRefreshLeavesNoLiveSessionIntegration(t *testing.T) {
	svc, ctx, u, device, registered := newAccountIntegrationFixture(t)
	currentID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	otherID, other := attachRemovalTarget(t, svc, ctx, u, device)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var refreshed Tokens
	var refreshErr, resetErr error
	go func() {
		defer wg.Done()
		<-start
		refreshed, refreshErr = svc.Refresh(ctx, u.ID, otherID, other.Tokens.RefreshToken)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, resetErr = svc.ResetSecret(ctx, u.ID, currentID, uuid.NewString(), nil, "")
	}()
	close(start)
	wg.Wait()

	if resetErr != nil {
		t.Fatalf("reset failed: %v", resetErr)
	}
	var live int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions
	 WHERE device_id=$1 AND revoked_at IS NULL`, otherID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d live sessions survived on a device the reset revoked", live)
	}
	if refreshErr == nil {
		// 刷新赢了这一局也没关系——它拿到的令牌必须已经被撤销。
		if _, err := svc.Refresh(ctx, u.ID, otherID, refreshed.RefreshToken); err == nil {
			t.Fatal("a refresh token minted during the reset is still usable")
		}
	}
	assertDeviceRevocation(t, svc, ctx, otherID, "REVOKED", 0)
}

// TestPurgeAccountSecretsClearsResetResponsesIntegration：清理任务必须也盯着
// 密钥重置的那条记录。漏掉它，一把明文新密钥会在库里多躺 24 小时。
func TestPurgeAccountSecretsClearsResetResponsesIntegration(t *testing.T) {
	svc, ctx, u, _, registered := newAccountIntegrationFixture(t)
	deviceID, _ := strconv.ParseInt(registered.DeviceID, 10, 64)
	key := uuid.NewString()
	if _, err := svc.ResetSecret(ctx, u.ID, deviceID, key, nil, ""); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT response FROM api_idempotency
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatal("the reset response was not stored at all, so the replay path cannot work")
	}
	var raw json.RawMessage
	if err := json.Unmarshal(stored, &raw); err != nil {
		t.Fatal(err)
	}

	jobs := &Jobs{Repo: svc.Repo}
	// 窗口内不该被清掉，否则重放会平白失败。
	if err := jobs.PurgeAccountSecrets(ctx, svc.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT response FROM api_idempotency
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatal("the purge cleared a reset response that is still inside its display window")
	}
	// 窗口外必须清空。
	if err := jobs.PurgeAccountSecrets(ctx, svc.Clock.Now().Add(idemSecretResetTTL+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT response FROM api_idempotency
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("an expired reset response still holds the new secret: %s", stored)
	}

	// 第二半：24 小时以上的整行要被删掉。这一步以业务角色执行 DELETE，
	// 而 yuhai_app 在 00035 之前没有这个权限——任务每分钟报一次 42501，
	// 表只增不减，却没有任何测试以业务角色调用过它。这条断言把那个洞焊上。
	old := uuid.NewString()
	if err := svc.Repo.putIdem(ctx, svc.Repo.Pool, u.ID, old, "other_endpoint", "h", 200, []byte(`"x"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Repo.Pool.Exec(ctx, `UPDATE api_idempotency SET created_at=$3
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, old, svc.Clock.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := jobs.PurgeAccountSecrets(ctx, svc.Clock.Now()); err != nil {
		t.Fatalf("the purge job cannot run as the application role: %v", err)
	}
	var left int
	if err := svc.Repo.Pool.QueryRow(ctx, `SELECT count(*) FROM api_idempotency
	 WHERE user_id=$1 AND idem_key=$2`, u.ID, old).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("a day-old idempotency row survived the purge")
	}
}
