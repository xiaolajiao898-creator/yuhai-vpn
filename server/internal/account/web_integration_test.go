package account

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/redisx"
)

func TestWebRegisterCreatesLoginableAccount(t *testing.T) {
	dbURL, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_REDIS_URL to isolated test services")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rdb, err := redisx.New(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	enc, err := fieldenc.New("web-integration", key)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Repo: &Repo{Pool: pool}, Signer: auth.NewSigner("web-integration", key), Enc: enc, Redis: rdb, Clock: clock.Real{}, Pepper: key}
	engine := newWebEngine(t, svc, rdb)

	// 服务层：幂等、冲突、过期，以及不绑定设备。
	address := testAddress(t)
	idem := uuid.NewString()
	raw := []byte(`{"address":" ` + address + ` ","password":"WebOnly99"}`)
	in := WebRegisterInput{Address: "  " + address + " ", Password: "WebOnly99"}
	res, status, err := svc.RegisterFromWeb(ctx, in, idem, raw, "ip-hash", "prefix-hash")
	if err != nil || status != http.StatusCreated || res.LoginAddress != address || res.AccountNo == "" || res.InviteCode == "" {
		t.Fatalf("register: %+v %d %v", res, status, err)
	}
	again, _, err := svc.RegisterFromWeb(ctx, in, idem, raw, "ip-hash", "prefix-hash")
	if err != nil || again.AccountNo != res.AccountNo {
		t.Fatalf("replay: %+v %v", again, err)
	}
	changed := []byte(`{"address":"` + address + `","password":"WebOnly99","counter":2}`)
	if _, _, err := svc.RegisterFromWeb(ctx, in, idem, changed, "", ""); codeOf(err) != httpx.ErrConflict {
		t.Fatalf("changed body: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE api_idempotency SET created_at = now() - interval '11 minutes' WHERE user_id=0 AND idem_key=$1`, idem); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RegisterFromWeb(ctx, in, idem, raw, "", ""); codeOf(err) != httpx.ErrNotFound {
		t.Fatalf("expired replay: %v", err)
	}
	var users int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE account_no=$1`, res.AccountNo).Scan(&users); err != nil || users != 1 {
		t.Fatalf("user count %d %v", users, err)
	}
	var rowText, channel string
	var acked bool
	var devices int
	if err := pool.QueryRow(ctx, `SELECT row_to_json(u)::text, register_channel, backup_acknowledged_at IS NOT NULL,
		(SELECT count(*) FROM devices d WHERE d.user_id=u.id) FROM users u WHERE account_no=$1`, res.AccountNo).
		Scan(&rowText, &channel, &acked, &devices); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, address) || strings.Contains(rowText, "WebOnly99") || !strings.Contains(rowText, `"password_hash":"$argon2id$`) {
		t.Fatal("address or password stored in plaintext, or password not hashed")
	}
	if channel != "WEB" || !acked || devices != 0 {
		t.Fatalf("channel=%s acked=%v devices=%d", channel, acked, devices)
	}
	var idemText string
	if err := pool.QueryRow(ctx, `SELECT response::text FROM api_idempotency WHERE user_id=0 AND idem_key=$1`, idem).Scan(&idemText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(idemText, address) || strings.Contains(idemText, "WebOnly99") {
		t.Fatal("idempotency response stored the address or password in plaintext")
	}
	var userID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE account_no=$1`, res.AccountNo).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.loginAddress(ctx, userID); err != nil || got != address {
		t.Fatalf("login address %q %v", got, err)
	}
	android, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "WebOnly99"}, "")
	if err != nil || android.AccountNo != res.AccountNo || android.Tokens.AccessToken == "" {
		t.Fatalf("android login: %+v %v", android, err)
	}
	windows, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "WINDOWS"), Address: address, Password: "WebOnly99"}, "")
	if err != nil || windows.AccountNo != res.AccountNo || windows.Tokens.AccessToken == "" {
		t.Fatalf("windows login: %+v %v", windows, err)
	}
	if _, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: address, Password: "WebOnly98"}, ""); codeOf(err) != httpx.ErrLoginInvalid {
		t.Fatalf("wrong password: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM devices WHERE user_id=$1 AND status='ACTIVE'`, userID).Scan(&devices); err != nil || devices != 2 {
		t.Fatalf("devices after app login: %d %v", devices, err)
	}

	// 无效邀请码仍然开户。
	badInviteAddr := testAddress(t)
	badInvite, _, err := svc.RegisterFromWeb(ctx, WebRegisterInput{Address: badInviteAddr, Password: "abc12345", InviteCode: "NOPE"}, uuid.NewString(), []byte("x"), "", "")
	if err != nil || badInvite.InviteBound {
		t.Fatalf("invalid invite: %+v %v", badInvite, err)
	}

	// HTTP：解题、跨域、不开设备、不发令牌，邀请码可以绑上。
	origin := webRegisterOrigin
	ip := testIPv4(t)
	httpAddress := testAddress(t)
	reg := registerOverHTTP(t, engine, ip, origin, httpAddress, "abc12345", "")
	if reg.AccountNo == "" || reg.LoginAddress != httpAddress {
		t.Fatalf("http result %+v", reg)
	}
	var httpDevices int
	var httpChannel string
	if err := pool.QueryRow(ctx, `SELECT register_channel, (SELECT count(*) FROM devices d WHERE d.user_id=u.id)
		FROM users u WHERE account_no=$1`, reg.AccountNo).Scan(&httpChannel, &httpDevices); err != nil {
		t.Fatal(err)
	}
	if httpChannel != "WEB" || httpDevices != 0 {
		t.Fatalf("http user channel=%s devices=%d", httpChannel, httpDevices)
	}
	logged, err := svc.LoginWithPassword(ctx, PasswordLoginInput{DeviceInfo: newDevice(t, "ANDROID"), Address: httpAddress, Password: "abc12345"}, "")
	if err != nil || logged.AccountNo != reg.AccountNo {
		t.Fatalf("login after http register: %+v %v", logged, err)
	}

	inviteeIP := testIPv4(t)
	invitee := registerOverHTTP(t, engine, inviteeIP, webRegisterOrigin, testAddress(t), "abc12345", res.InviteCode)
	if !invitee.InviteBound {
		t.Fatalf("invite not bound: %+v", invitee)
	}
	var relations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invite_relations r JOIN users u ON u.id=r.invitee_user_id
		WHERE u.account_no=$1 AND r.invite_code=$2 AND r.bind_method='MANUAL'`, invitee.AccountNo, res.InviteCode).Scan(&relations); err != nil || relations != 1 {
		t.Fatalf("relation %d %v", relations, err)
	}

	// 同一地址不能再开一个户。
	dupIP := testIPv4(t)
	dupBody := registerBody(t, engine, dupIP, webRegisterOrigin, httpAddress, "abc12345", "")
	dup := webDo(engine, http.MethodPost, "/v1/auth/web/register", dupIP, webRegisterOrigin, dupBody, "")
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d %s", dup.Code, dup.Body.String())
	}
	if code, _ := errorMessage(t, dup); code != int(httpx.ErrAddressTaken) {
		t.Fatal(code)
	}

	// 旧的设备自签注册接口不被这次的 CORS 放开。
	legacy := webDo(engine, http.MethodPost, "/v1/auth/register", testIPv4(t), webRegisterOrigin, []byte(`{}`), "")
	if legacy.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("legacy register echoed CORS: %q", legacy.Header().Get("Access-Control-Allow-Origin"))
	}
}

func registerOverHTTP(t *testing.T, h http.Handler, ip, origin, address, password, invite string) WebRegisterResult {
	t.Helper()
	body := registerBody(t, h, ip, origin, address, password, invite)
	w := webDo(h, http.MethodPost, "/v1/auth/web/register", ip, origin, body, "")
	if w.Code != http.StatusCreated || w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("register http: %d acao=%q cache=%q %s", w.Code, w.Header().Get("Access-Control-Allow-Origin"), w.Header().Get("Cache-Control"), w.Body.String())
	}
	assertWebCORS(t, w)
	if bytesContainToken(w.Body.Bytes()) {
		t.Fatalf("web register returned a token or account secret: %s", w.Body.String())
	}
	var env struct {
		Data WebRegisterResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func registerBody(t *testing.T, h http.Handler, ip, origin, address, password, invite string) []byte {
	t.Helper()
	ch := mustIssue(t, h, ip)
	if ch.Algorithm != webPoWAlgorithm {
		t.Fatal(ch.Algorithm)
	}
	counter := solveWebPoW(t, ch.ChallengeID, ch.Nonce, ch.Difficulty)
	body, err := json.Marshal(map[string]any{
		"address": address, "password": password, "invite_code": invite,
		"yh_hp": "", "challenge_id": ch.ChallengeID, "counter": counter,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func bytesContainToken(b []byte) bool {
	s := string(b)
	return strings.Contains(s, "access_token") || strings.Contains(s, "YH-") || strings.Contains(s, "account_secret")
}
