package account

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/tron"
)

// fakeChain 模拟链上：按「金额」登记已确认的转账。
type fakeChain struct {
	mu        sync.Mutex
	transfers map[int64]tron.Transfer // amount_sun → 交易
	from      string
	calls     int
	fail      error
}

func (f *fakeChain) FindTRXTransfer(_ context.Context, from, to string, amountSun int64, since time.Time) (tron.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return tron.Transfer{}, f.fail
	}
	if from != f.from {
		return tron.Transfer{}, tron.ErrTransferNotFound
	}
	t, ok := f.transfers[amountSun]
	if !ok || t.BlockTime.Before(since) {
		return tron.Transfer{}, tron.ErrTransferNotFound
	}
	return t, nil
}

func (f *fakeChain) pay(amount int64, txid string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transfers[amount] = tron.Transfer{TxID: txid, AmountSun: amount, BlockTime: at}
}

// testAddress 生成一个校验和正确、每次不同的 TRON 地址。
func testAddress(t *testing.T) string {
	p := make([]byte, 21)
	p[0] = 0x41
	if _, err := rand.Read(p[1:]); err != nil {
		t.Fatal(err)
	}
	for {
		a := tron.EncodeAddress(p)
		if tron.ValidAddress(a) {
			return a
		}
		p[1]++
	}
}

func newDevice(t *testing.T, platform string) DeviceInfo {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return DeviceInfo{DeviceUID: uuid.NewString(), DevicePubKey: base64.StdEncoding.EncodeToString(der), Platform: platform, Model: "integration", OSVersion: "1", AppVersion: "1.1.0+110"}
}

func codeOf(err error) httpx.Code {
	var e *httpx.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func TestAddressPasswordLifecycleIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to an isolated migrated database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := fieldenc.New("integration", key)
	if err != nil {
		t.Fatal(err)
	}
	address := testAddress(t)
	payTo := testAddress(t)
	chain := &fakeChain{transfers: map[int64]tron.Transfer{}, from: address}
	svc := &Service{Repo: &Repo{Pool: pool}, Signer: auth.NewSigner("integration", key), Enc: enc, Clock: clock.Real{}, Pepper: key,
		Tron: chain, ResetPayTo: payTo}

	// ---- 注册 ----
	d1 := newDevice(t, "WINDOWS")
	in := AddressRegisterInput{DeviceInfo: d1, Address: "  " + address + " ", Password: "abc12345"}
	raw, _ := json.Marshal(in)
	idem := uuid.NewString()
	reg, status, err := svc.RegisterWithAddress(ctx, in, idem, raw, "", "")
	if err != nil || status != 201 || reg.LoginAddress != address || reg.Tokens.AccessToken == "" {
		t.Fatalf("register: %+v %d %v", reg, status, err)
	}
	again, _, err := svc.RegisterWithAddress(ctx, in, idem, raw, "", "")
	if err != nil || again.AccountNo != reg.AccountNo || again.Tokens.AccessToken != reg.Tokens.AccessToken {
		t.Fatalf("idempotent replay differs: %v", err)
	}
	if _, _, err := svc.RegisterWithAddress(ctx, in, idem, []byte(`{"x":1}`), "", ""); codeOf(err) != httpx.ErrConflict {
		t.Fatalf("changed body: %v", err)
	}
	dup := AddressRegisterInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "zzz99999"}
	rawDup, _ := json.Marshal(dup)
	if _, _, err := svc.RegisterWithAddress(ctx, dup, uuid.NewString(), rawDup, "", ""); codeOf(err) != httpx.ErrAddressTaken {
		t.Fatalf("duplicate address: %v", err)
	}
	for _, bad := range []AddressRegisterInput{
		{DeviceInfo: newDevice(t, "ANDROID"), Address: "0x1234", Password: "abc12345"},
		{DeviceInfo: newDevice(t, "ANDROID"), Address: testAddress(t), Password: "short1"},
		{DeviceInfo: newDevice(t, "ANDROID"), Address: testAddress(t), Password: "onlyletters"},
		{DeviceInfo: newDevice(t, "LINUX"), Address: testAddress(t), Password: "abc12345"},
	} {
		b, _ := json.Marshal(bad)
		if _, _, err := svc.RegisterWithAddress(ctx, bad, uuid.NewString(), b, "", ""); codeOf(err) != httpx.ErrBadRequest {
			t.Fatalf("bad input accepted: %+v %v", bad.Address, err)
		}
	}
	// 数据库里不出现明文地址。
	var rowText string
	if err := pool.QueryRow(ctx, `SELECT row_to_json(u)::text FROM users u WHERE account_no=$1`, reg.AccountNo).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, address) || !strings.Contains(rowText, `"password_hash":"$argon2id$`) {
		t.Fatal("address stored in plaintext or password not hashed")
	}
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE account_no=$1`, reg.AccountNo).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.loginAddress(ctx, userID); err != nil || got != address {
		t.Fatalf("login address %q %v", got, err)
	}

	// ---- 登录 ----
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "abc12346"}, ""); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: testAddress(t), Password: "abc12345"}, ""); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("unknown address: %v", err)
	}
	d2 := newDevice(t, "MACOS")
	login, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: d2, Address: address, Password: "abc12345"}, "")
	if err != nil || login.AccountNo != reg.AccountNo {
		t.Fatalf("login: %+v %v", login, err)
	}

	// ---- 改密码 ----
	if err := svc.ChangePassword(ctx, userID, ChangePasswordInput{OldPassword: "wrong0000", NewPassword: "new12345"}); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("change with wrong old: %v", err)
	}
	if err := svc.ChangePassword(ctx, userID, ChangePasswordInput{OldPassword: "abc12345", NewPassword: "new12345"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "abc12345"}, ""); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("old password still works: %v", err)
	}

	// ---- 忘记密码 ----
	if _, err := svc.StartPasswordReset(ctx, ResetStartInput{Address: testAddress(t)}); codeOf(err) != httpx.ErrNotFound {
		t.Fatalf("reset unknown address: %v", err)
	}
	attacker, err := svc.StartPasswordReset(ctx, ResetStartInput{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := svc.StartPasswordReset(ctx, ResetStartInput{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	if mine.AmountSun == attacker.AmountSun || mine.AmountSun <= resetBaseSun || mine.AmountSun > resetBaseSun+resetJitterSun || mine.PayTo != payTo {
		t.Fatalf("challenge amounts: %+v %+v", mine, attacker)
	}
	if mine.Amount != formatSun(mine.AmountSun) || !strings.HasPrefix(mine.Amount, "1.00") {
		t.Fatalf("amount text %q", mine.Amount)
	}
	d3 := newDevice(t, "ANDROID")
	complete := ResetCompleteInput{DeviceInfo: d3, ChallengeID: mine.ChallengeID, NewPassword: "reset123"}
	if _, err := svc.CompletePasswordReset(ctx, complete, ""); codeOf(err) != httpx.ErrResetNotPaid {
		t.Fatalf("before payment: %v", err)
	}
	// 用户按自己挑战的金额转账；攻击者的挑战金额不同，认领不了这笔。
	txid := "tx-" + uuid.NewString()
	chain.pay(mine.AmountSun, txid, time.Now())
	if _, err := svc.CompletePasswordReset(ctx, ResetCompleteInput{DeviceInfo: newDevice(t, "ANDROID"), ChallengeID: attacker.ChallengeID, NewPassword: "evil1234"}, ""); codeOf(err) != httpx.ErrResetNotPaid {
		t.Fatalf("attacker claimed the transfer: %v", err)
	}
	chain.fail = errors.New("trongrid down")
	if _, err := svc.CompletePasswordReset(ctx, complete, ""); codeOf(err) != httpx.ErrMaintenance {
		t.Fatalf("chain outage: %v", err)
	}
	chain.fail = nil
	res, err := svc.CompletePasswordReset(ctx, complete, "")
	if err != nil || res.AccountNo != reg.AccountNo {
		t.Fatalf("complete: %+v %v", res, err)
	}
	// 旧设备全部下线，只剩发起设备；新密码可用、旧密码失效。
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM devices WHERE user_id=$1 AND status='ACTIVE'`, userID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active devices after reset: %d %v", active, err)
	}
	var liveSessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth_sessions s JOIN devices d ON d.id=s.device_id
 WHERE s.user_id=$1 AND s.revoked_at IS NULL AND d.device_uid<>$2`, userID, d3.DeviceUID).Scan(&liveSessions); err != nil || liveSessions != 0 {
		t.Fatalf("old sessions alive: %d %v", liveSessions, err)
	}
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "new12345"}, ""); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("pre-reset password still works: %v", err)
	}
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "reset123"}, ""); err != nil {
		t.Fatalf("new password: %v", err)
	}
	// 同一挑战不能再用；攻击者挑战已被作废；同一笔交易不能再用于新的挑战。
	if _, err := svc.CompletePasswordReset(ctx, ResetCompleteInput{DeviceInfo: newDevice(t, "ANDROID"), ChallengeID: mine.ChallengeID, NewPassword: "again123"}, ""); codeOf(err) != httpx.ErrConflict {
		t.Fatalf("reuse challenge: %v", err)
	}
	next, err := svc.StartPasswordReset(ctx, ResetStartInput{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	chain.pay(next.AmountSun, txid, time.Now().Add(time.Second))
	if _, err := svc.CompletePasswordReset(ctx, ResetCompleteInput{DeviceInfo: newDevice(t, "ANDROID"), ChallengeID: next.ChallengeID, NewPassword: "again123"}, ""); codeOf(err) != httpx.ErrConflict {
		t.Fatalf("reuse txid: %v", err)
	}
	var st string
	if err := pool.QueryRow(ctx, `SELECT status FROM password_reset_challenges WHERE id=$1`, attacker.ChallengeID).Scan(&st); err != nil || st != "EXPIRED" {
		t.Fatalf("attacker challenge status %q %v", st, err)
	}

	// ---- 找回关闭时 ----
	off := *svc
	off.ResetPayTo = ""
	if _, err := off.StartPasswordReset(ctx, ResetStartInput{Address: address}); codeOf(err) != httpx.ErrMaintenance {
		t.Fatalf("disabled reset: %v", err)
	}

	// ---- 并发注册同一地址：只有一个成功 ----
	raceAddr := testAddress(t)
	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := AddressRegisterInput{DeviceInfo: newDevice(t, "ANDROID"), Address: raceAddr, Password: "abc12345"}
			b, _ := json.Marshal(r)
			_, _, errs[i] = svc.RegisterWithAddress(ctx, r, uuid.NewString(), b, "", "")
		}(i)
	}
	wg.Wait()
	okCount := 0
	for _, e := range errs {
		switch {
		case e == nil:
			okCount++
		case codeOf(e) != httpx.ErrAddressTaken:
			t.Fatalf("unexpected race error: %v", e)
		}
	}
	if okCount != 1 {
		t.Fatalf("race: %d registrations succeeded", okCount)
	}
}

func TestPasswordRules(t *testing.T) {
	for p, ok := range map[string]bool{"abc12345": true, "ABCdef99": true, "12345678": false, "abcdefgh": false, "a1": false, "密码安全12ab": true} {
		if (validatePassword(p) == nil) != ok {
			t.Fatalf("%q: want ok=%v", p, ok)
		}
	}
	h, err := hashPassword("abc12345")
	if err != nil || !verifyPassword(h, "abc12345") || verifyPassword(h, "abc12346") || verifyPassword("garbage", "abc12345") {
		t.Fatal("hash/verify broken")
	}
	if formatSun(1_004_372) != "1.004372" || formatSun(1_000_001) != "1.000001" {
		t.Fatal("formatSun")
	}
}
