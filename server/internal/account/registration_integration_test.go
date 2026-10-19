package account

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/deposit"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/order"
)

// 只在显式提供测试数据库时执行；不连接生产配置 DATABASE_URL。
func TestRegistrationConcurrentReplayIntegration(t *testing.T) {
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
	svc := &Service{Repo: &Repo{Pool: pool}, Signer: auth.NewSigner("integration", key), Enc: enc, Clock: clock.Real{}, Pepper: key}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	in := RegisterInput{DeviceInfo: DeviceInfo{DeviceUID: uuid.NewString(), DevicePubKey: base64.StdEncoding.EncodeToString(der), Platform: "ANDROID", Model: "integration", OSVersion: "16", AppVersion: "1.0.0+100"}}
	raw, _ := json.Marshal(in)
	idem := uuid.NewString()
	const n = 8
	results := make([]RegisterResult, n)
	errs := make([]error, n)
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], statuses[i], errs[i] = svc.Register(ctx, in, idem, raw, "", "")
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errs[i] != nil || statuses[i] != 201 {
			t.Fatalf("attempt %d: status %d error %v", i, statuses[i], errs[i])
		}
		if results[i].AccountNo != results[0].AccountNo || results[i].AccountSecret != results[0].AccountSecret {
			t.Fatal("replay created a different account")
		}
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT response::text FROM api_idempotency WHERE user_id=0 AND idem_key=$1", idem).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var cipher []byte
	if err := json.Unmarshal([]byte(stored), &cipher); err != nil || len(cipher) == 0 {
		t.Fatal("ciphertext not valid JSON")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM devices WHERE device_uid=$1", in.DeviceUID).Scan(&count); err != nil || count != 1 {
		t.Fatal("expected exactly one device", count, err)
	}
	if _, _, err := svc.Register(ctx, in, idem, []byte(`{"changed":true}`), "", ""); err == nil {
		t.Fatal("changed body accepted under same key")
	}
	// 真实数据库中的到账、重复处理和重复付款不重复入账。
	// 收款地址不随迁移入库（那是资金系统签发的）。测试自己放一个短期 MOCK 地址。
	if _, err := pool.Exec(ctx, `INSERT INTO receive_addresses(network,address,signature,sig_key_id,valid_until,status)
		VALUES('MOCK',$1,'integration-only','integration',now()+interval '1 day','ACTIVE')`, "reg-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, "SELECT user_id FROM devices WHERE device_uid=$1", in.DeviceUID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	orders := &order.Service{Pool: pool, Clock: clock.Real{}}
	o, err := orders.Create(ctx, userID, order.CreateInput{OrderType: "TOPUP", Amount: "10", PayMethod: "USDT_TRC20"}, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	deposits := &deposit.Service{Pool: pool, Clock: clock.Real{}}
	paid, err := deposits.MockDeposit(ctx, deposit.MockInput{OrderNo: o.OrderNo, Amount: o.PayAmount})
	if err != nil || paid.Outcome != "MATCHED" {
		t.Fatal(paid, err)
	}
	var firstBalance int64
	if err := pool.QueryRow(ctx, "SELECT available_micro FROM user_balances WHERE user_id=$1", userID).Scan(&firstBalance); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, e := deposits.Settle(ctx, paid.TransferID)
			if e == nil && r.Outcome != "SKIPPED" {
				e = fmt.Errorf("replay outcome %s", r.Outcome)
			}
			errs[i] = e
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	duplicate, err := deposits.MockDeposit(ctx, deposit.MockInput{OrderNo: o.OrderNo, Amount: o.PayAmount})
	if err != nil || duplicate.Reason != "DUPLICATE_PAYMENT" {
		t.Fatal(duplicate, err)
	}
	var currentBalance int64
	if err := pool.QueryRow(ctx, "SELECT available_micro FROM user_balances WHERE user_id=$1", userID).Scan(&currentBalance); err != nil || currentBalance != firstBalance {
		t.Fatal("duplicate credit", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM balance_ledger WHERE user_id=$1", userID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate ledger rows", count, err)
	}

	// 加密响应写入失败必须回滚账户与设备，不能留下半成品账户。
	badEnc, err := fieldenc.New(strings.Repeat("x", 256), key)
	if err != nil {
		t.Fatal(err)
	}
	svc.Enc = badEnc
	in.DeviceUID = uuid.NewString()
	raw, _ = json.Marshal(in)
	if _, _, err := svc.Register(ctx, in, uuid.NewString(), raw, "", ""); err == nil {
		t.Fatal("expected encryption failure")
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM devices WHERE device_uid=$1", in.DeviceUID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed registration left a device", count, err)
	}

}
