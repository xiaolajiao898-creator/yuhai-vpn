// devclient：模拟一台设备走通 注册 → /v1/me → 刷新令牌 → 恢复 → 退出，用于联调与冒烟测试。
//
//	go run ./cmd/devclient -base http://localhost:8080
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/auth"
)

type device struct {
	uid   string
	priv  *ecdsa.PrivateKey
	spki  string
	token string
}

func newDevice() *device {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	return &device{uid: uuid.NewString(), priv: priv, spki: base64.StdEncoding.EncodeToString(der)}
}

func (d *device) info() map[string]any {
	return map[string]any{"device_uid": d.uid, "device_pubkey": d.spki, "platform": "ANDROID", "model": "devclient", "os_version": "16", "app_version": "1.0.0+100"}
}

func (d *device) do(base, method, path string, body any, idem bool) (int, map[string]any) {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	u, _ := url.Parse(base + path)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := uuid.NewString()
	canonical := auth.CanonicalRequest(method, u.Path, u.Query(), ts, nonce, d.uid, raw)
	digest := sha256.Sum256([]byte(canonical))
	sig, _ := ecdsa.SignASN1(rand.Reader, d.priv, digest[:])
	req, _ := http.NewRequest(method, u.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Version", "1.0.0+100")
	req.Header.Set("X-Platform", "ANDROID")
	req.Header.Set(auth.HeaderDeviceID, d.uid)
	req.Header.Set(auth.HeaderTimestamp, ts)
	req.Header.Set(auth.HeaderNonce, nonce)
	req.Header.Set(auth.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	if idem {
		req.Header.Set("Idempotency-Key", uuid.NewString())
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		fmt.Println("request failed:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	fmt.Printf("%s %s → %d\n", method, path, res.StatusCode)
	if res.StatusCode >= 500 {
		os.Exit(1)
	}
	return res.StatusCode, out
}

func main() {
	base := flag.String("base", "http://localhost:8080", "app-api 地址")
	admin := flag.String("admin", "http://localhost:8081", "admin-api 地址（模拟到账）")
	address := flag.String("address", "", "只跑「地址 + 密码」注册登录冒烟（填一个 TRON 地址）")
	flag.Parse()
	if *address != "" {
		smokeAddress(*base, *address)
		return
	}

	d := newDevice()
	code, out := d.do(*base, "POST", "/v1/auth/register", d.info(), true)
	if code != 201 {
		os.Exit(1)
	}
	data := out["data"].(map[string]any)
	secret := data["account_secret"].(string)
	tokens := data["tokens"].(map[string]any)
	d.token = tokens["access_token"].(string)
	refresh := tokens["refresh_token"].(string)
	// 账户密钥与令牌只在内存中使用，禁止打印到联调日志。

	d.do(*base, "GET", "/v1/me", nil, false)
	d.do(*base, "POST", "/v1/account/backup-ack", nil, true)

	_, out = d.do(*base, "POST", "/v1/auth/refresh", map[string]any{"refresh_token": refresh}, false)
	nt := out["data"].(map[string]any)
	d.token = nt["access_token"].(string)
	// 旧刷新令牌重放 → 40103 且整族作废
	d.do(*base, "POST", "/v1/auth/refresh", map[string]any{"refresh_token": refresh}, false)

	// 套餐 → 报价 → USDT 下单 → 轮询 → 取消 → 余额支付（余额为 0 → 40201）
	d.do(*base, "GET", "/v1/nodes", nil, false)
	d.do(*base, "GET", "/v1/plans", nil, false)
	_, out = d.do(*base, "POST", "/v1/plans/quote", map[string]any{"plan_code": "STD_1M"}, false)
	if q, ok := out["data"].(map[string]any); ok {
		_, o := d.do(*base, "POST", "/v1/orders", map[string]any{"order_type": "PLAN", "quote_id": q["quote_id"], "pay_method": "USDT_TRC20"}, true)
		if od, ok := o["data"].(map[string]any); ok {
			no := od["order_no"].(string)
			fmt.Printf("请向 %s 转入 %s USDT（唯一尾数）\n", od["receive_address"], od["pay_amount"])
			d.do(*base, "GET", "/v1/orders/"+no, nil, false)
			// 模拟到账：先付错金额（进异常队列），再付正确金额（开通）
			mock(*admin, no, "1.0000")
			mock(*admin, no, od["pay_amount"].(string))
			mock(*admin, no, od["pay_amount"].(string)) // 重复付款 → DUPLICATE_PAYMENT 异常，不重复开通
			_, finished := d.do(*base, "GET", "/v1/orders/"+no, nil, false)
			orderData, ok := finished["data"].(map[string]any)
			if !ok || orderData["status"] != "COMPLETED" {
				fmt.Println("FAIL: 模拟到账未完成订单")
				os.Exit(1)
			}
			d.do(*base, "GET", "/v1/balance/ledger?limit=1", nil, false)
			d.do(*base, "GET", "/v1/entitlements/ledger", nil, false)
			d.do(*base, "GET", "/v1/me", nil, false)
			d.do(*base, "POST", "/v1/orders/"+no+"/cancel", nil, true) // 已完成 → 40905
		}
		_, out = d.do(*base, "POST", "/v1/plans/quote", map[string]any{"plan_code": "STD_1M"}, false)
		q = out["data"].(map[string]any)
		d.do(*base, "POST", "/v1/orders", map[string]any{"order_type": "PLAN", "quote_id": q["quote_id"], "pay_method": "BALANCE"}, true)
	}
	d.do(*base, "POST", "/v1/orders", map[string]any{"order_type": "TOPUP", "amount": "50", "pay_method": "USDT_TRC20"}, true)
	d.do(*base, "GET", "/v1/orders", nil, false)

	// 第二、三台设备用账户密钥恢复（无套餐时上限 2 台，第三台 → 40902）
	d2 := newDevice()
	body := d2.info()
	body["account_secret"] = secret
	d2.do(*base, "POST", "/v1/auth/restore", body, false)
	d2 = newDevice()
	body = d2.info()
	body["account_secret"] = secret
	code, out = d2.do(*base, "POST", "/v1/auth/restore", body, false)
	if code == 409 {
		details := out["error"].(map[string]any)["details"].(map[string]any)
		devs := details["devices"].([]any)
		body["restore_ticket"] = details["restore_ticket"]
		body["remove_device_id"] = devs[0].(map[string]any)["id"]
		_, out = d2.do(*base, "POST", "/v1/auth/restore/replace-device", body, true)
		d2.token = out["data"].(map[string]any)["tokens"].(map[string]any)["access_token"].(string)
		d2.do(*base, "GET", "/v1/devices", nil, false)
		// 被移除的第一台设备再访问 → 40107
		d.do(*base, "GET", "/v1/me", nil, false)
		d2.do(*base, "POST", "/v1/auth/logout", nil, true)
	}
}

func mock(admin, orderNo, amount string) {
	raw, _ := json.Marshal(map[string]any{"order_no": orderNo, "amount": amount})
	cookie, csrf := os.Getenv("YH_DEV_ADMIN_COOKIE"), os.Getenv("YH_DEV_ADMIN_CSRF")
	if cookie == "" || csrf == "" {
		fmt.Println("模拟到账需要已登录且具备 dev.tools 权限的管理员会话（YH_DEV_ADMIN_COOKIE / YH_DEV_ADMIN_CSRF）")
		os.Exit(1)
	}
	req, err := http.NewRequest("POST", admin+"/admin/v1/dev/mock-deposit", bytes.NewReader(raw))
	if err != nil {
		fmt.Println("invalid admin URL")
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(&http.Cookie{Name: "yh_admin", Value: cookie})
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		fmt.Println("mock-deposit failed:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	fmt.Printf("MOCK DEPOSIT %s %s → %d %s\n", orderNo, amount, res.StatusCode, truncate(string(b), 200))
	if res.StatusCode != 200 {
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// smokeAddress 走一遍「地址 + 密码」：注册 → /v1/me → 改密码 → 退出 → 旧密码失败 → 新密码登录 → 发起找回。
func smokeAddress(base, address string) {
	expect := func(got, want int, what string) {
		if got != want {
			fmt.Printf("FAIL: %s 期望 %d 实际 %d\n", what, want, got)
			os.Exit(1)
		}
	}
	d := newDevice()
	body := d.info()
	body["address"] = address
	body["password"] = "smoke1234"
	code, out := d.do(base, "POST", "/v1/auth/address/register", body, true)
	expect(code, 201, "注册")
	d.token = out["data"].(map[string]any)["tokens"].(map[string]any)["access_token"].(string)
	code, out = d.do(base, "GET", "/v1/me", nil, false)
	expect(code, 200, "我的")
	if out["data"].(map[string]any)["login_address"] != address {
		fmt.Println("FAIL: /v1/me 没有返回登录地址")
		os.Exit(1)
	}
	code, _ = d.do(base, "POST", "/v1/account/password", map[string]any{"old_password": "smoke1234", "new_password": "smoke5678"}, true)
	expect(code, 200, "改密码")
	code, _ = d.do(base, "POST", "/v1/auth/logout", nil, true)
	expect(code, 204, "退出")
	d2 := newDevice()
	b2 := d2.info()
	b2["address"] = address
	b2["password"] = "smoke1234"
	code, _ = d2.do(base, "POST", "/v1/auth/address/login", b2, false)
	expect(code, 401, "旧密码登录")
	d2 = newDevice()
	b2 = d2.info()
	b2["address"] = address
	b2["password"] = "smoke5678"
	code, _ = d2.do(base, "POST", "/v1/auth/address/login", b2, false)
	expect(code, 200, "新密码登录")
	raw, _ := json.Marshal(map[string]any{"address": address})
	res, err := http.Post(base+"/v1/auth/password-reset/start", "application/json", bytes.NewReader(raw))
	if err != nil {
		fmt.Println("FAIL: 发起找回", err)
		os.Exit(1)
	}
	rb, _ := io.ReadAll(res.Body)
	res.Body.Close()
	fmt.Printf("POST /v1/auth/password-reset/start → %d %s\n", res.StatusCode, truncate(string(rb), 160))
	fmt.Println("地址 + 密码冒烟完成")
}
