package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redis "github.com/redis/go-redis/v9"
	"github.com/yuhai-vpn/server/internal/account"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/invite"
	"github.com/yuhai-vpn/server/internal/redisx"
)

type apiInviteClock struct{ now time.Time }

func (c apiInviteClock) Now() time.Time { return c.now }

// A disposable schema and random Redis key identities keep the HTTP test away
// from business data. No worker is started and fixture rules cannot be used by
// the public schema. All database and Redis keys are cleaned up after the test.
func TestAppInviteSignedFlowWithRewardsPausedIntegration(t *testing.T) {
	dsn, ownerDSN, redisDSN := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_MIGRATE_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dsn == "" || ownerDSN == "" || redisDSN == "" {
		t.Skip("set TEST_DATABASE_URL, TEST_MIGRATE_DATABASE_URL and TEST_REDIS_URL to isolated test services")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	appCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database")
	}
	ownerCfg, err := pgxpool.ParseConfig(ownerDSN)
	if err != nil {
		t.Fatal("invalid fixture database")
	}
	if appCfg.ConnConfig.Host != ownerCfg.ConnConfig.Host || appCfg.ConnConfig.Port != ownerCfg.ConnConfig.Port || appCfg.ConnConfig.Database != ownerCfg.ConnConfig.Database {
		t.Fatal("fixture databases must match")
	}
	owner, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	schema := "api_invite_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = owner.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := owner.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	for _, table := range []string{"users", "devices", "invite_relations", "invite_reward_rules", "invite_rewards", "orders", "api_idempotency", "blacklist"} {
		if _, err = owner.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, table}.Sanitize()+" (LIKE "+pgx.Identifier{"public", table}.Sanitize()+" INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	role := pgx.Identifier{appCfg.ConnConfig.User}.Sanitize()
	for _, q := range []string{"GRANT USAGE ON SCHEMA " + quoted + " TO " + role, "GRANT SELECT,INSERT,UPDATE ON ALL TABLES IN SCHEMA " + quoted + " TO " + role, "GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA " + quoted + " TO " + role} {
		if _, err = owner.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	appCfg.ConnConfig.RuntimeParams["search_path"] = schema + ",pg_catalog"
	pool, err := pgxpool.NewWithConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rdb, err := redisx.New(redisDSN)
	if err != nil {
		t.Fatal("invalid test Redis")
	}
	if err = rdb.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	redisOptions, err := redis.ParseURL(redisDSN)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRedis := redis.NewClient(redisOptions)
	defer cleanupRedis.Close()
	clock := apiInviteClock{time.Now().UTC()}
	randomID := func() int64 {
		var b [8]byte
		if _, e := rand.Read(b[:]); e != nil {
			t.Fatal(e)
		}
		return int64(binary.BigEndian.Uint64(b[:])&((1<<50)-1)) + 1
	}
	newUser := func() (int64, string) {
		id := randomID()
		code, e := ids.NewInviteCode()
		if e != nil {
			t.Fatal(e)
		}
		no, e := ids.NewAccountNo()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `INSERT INTO users(id,account_no,secret_lookup,invite_code,created_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,$5)`, id, no, uuid.NewString(), code, clock.now); e != nil {
			t.Fatal(e)
		}
		return id, code
	}
	_, inviterCode := newUser()
	userID, _ := newUser()
	deviceID := randomID()
	deviceUID := uuid.NewString()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(der)
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,device_uid,pubkey,platform) OVERRIDING SYSTEM VALUE VALUES($1,$2,$3,$4,'ANDROID')`, deviceID, userID, deviceUID, pub); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	if _, err = rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	signer := auth.NewSigner("test-invite", seed)
	token, err := signer.Sign(auth.AccessClaims{Subject: userID, DeviceID: deviceID, SecretVersion: 1, KeyThumbprint: auth.Thumbprint(pub), IssuedAt: clock.now.Unix(), ExpiresAt: clock.now.Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	mw := &auth.Middleware{Signer: signer, Store: &account.Repo{Pool: pool}, Redis: rdb, Clock: clock}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	_ = router.SetTrustedProxies(nil)
	h := appInviteHandler(pool, clock, mw, false, "")
	h.Mount(router.Group("/v1"))
	keys := []string{"rl:invite_bind_user:" + strconv.FormatInt(userID, 10)}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := cleanupRedis.Del(c, keys...).Err(); err != nil {
			t.Error(err)
		}
	}()
	call := func(method, path, body, signedBody, idem, nonce string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		if nonce != "" {
			ts := strconv.FormatInt(clock.now.Unix(), 10)
			canonical := auth.CanonicalRequest(method, req.URL.Path, req.URL.Query(), ts, nonce, deviceUID, []byte(signedBody))
			digest := sha256.Sum256([]byte(canonical))
			sig, e := ecdsa.SignASN1(rand.Reader, key, digest[:])
			if e != nil {
				t.Fatal(e)
			}
			req.Header.Set("X-Device-Id", deviceUID)
			req.Header.Set("X-Timestamp", ts)
			req.Header.Set("X-Nonce", nonce)
			req.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(sig))
			keys = append(keys, "nonce:"+strconv.FormatInt(deviceID, 10)+":"+nonce)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	data := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var v struct {
			Data map[string]any `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v.Data
	}
	requireStatus := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("want HTTP %d, got %d: %s", want, w.Code, w.Body.String())
		}
	}
	requireStatus(call("GET", "/v1/invite", "", "", "", "", false), 401)
	summary := call("GET", "/v1/invite", "", "", "", "", true)
	requireStatus(summary, 200)
	if d := data(summary); d["binding_enabled"] != true || d["can_bind"] != true || d["rewards_enabled"] != false {
		t.Fatal("unexpected launch state", d)
	}
	body := `{"code":"` + inviterCode + `","method":"MANUAL"}`
	idem, nonce := uuid.NewString(), uuid.NewString()
	requireStatus(call("POST", "/v1/invite/bind", body, body, idem, "", true), 401)
	requireStatus(call("POST", "/v1/invite/bind", body, `{}`, idem, uuid.NewString(), true), 401)
	first := call("POST", "/v1/invite/bind", body, body, idem, nonce, true)
	requireStatus(first, 200)
	if data(first)["bound"] != true || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(first.Body.String())
	}
	requireStatus(call("POST", "/v1/invite/bind", body, body, idem, nonce, true), 401)
	replay := call("POST", "/v1/invite/bind", body, body, idem, uuid.NewString(), true)
	requireStatus(replay, 200)
	if data(replay)["bound"] != true {
		t.Fatal("retry lost committed result")
	}
	changed := `{"code":"` + inviterCode + `","method":"CLIPBOARD"}`
	requireStatus(call("POST", "/v1/invite/bind", changed, changed, idem, uuid.NewString(), true), 409)
	foreign := `{"code":"` + inviterCode + `","method":"MANUAL","user_id":1}`
	requireStatus(call("POST", "/v1/invite/bind", foreign, foreign, uuid.NewString(), uuid.NewString(), true), 400)
	requireStatus(call("POST", "/v1/invite/bind", body, body, uuid.NewString(), uuid.NewString(), true), 409)
	limited := call("POST", "/v1/invite/bind", body, body, uuid.NewString(), uuid.NewString(), true)
	requireStatus(limited, 429)
	if limited.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit missing retry guidance")
	}
	summary = call("GET", "/v1/invite", "", "", "", "", true)
	requireStatus(summary, 200)
	if d := data(summary); d["bind_status"] != "BOUND" || d["can_bind"] != false || d["rewards_enabled"] != false {
		t.Fatal(d)
	}
	requireStatus(call("GET", "/v1/invite/rewards", "", "", "", "", true), 200)
	var relations, rewards int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invite_relations WHERE invitee_user_id=$1),(SELECT count(*) FROM invite_rewards)`, userID).Scan(&relations, &rewards); err != nil || relations != 1 || rewards != 0 {
		t.Fatal("binding altered reward state", relations, rewards, err)
	}
	// Enabled rules inside this disposable schema cannot override a paused gate.
	if _, err = pool.Exec(ctx, `INSERT INTO invite_reward_rules(name,trigger_event,beneficiary,reward_type,reward_value,status,created_at) VALUES('isolated test','INVITEE_FIRST_PAID','INVITER','DAYS',7,'ENABLED',$1)`, clock.now); err != nil {
		t.Fatal(err)
	}
	summary = call("GET", "/v1/invite", "", "", "", "", true)
	requireStatus(summary, 200)
	if data(summary)["rewards_enabled"] != false {
		t.Fatal("rule bypassed closed runtime gate")
	}
	reader := h.Svc.(*invite.Service)
	reader.RewardsEnabled = true
	summary = call("GET", "/v1/invite", "", "", "", "", true)
	requireStatus(summary, 200)
	if data(summary)["rewards_enabled"] != true {
		t.Fatal("active fixture rule not reflected")
	}
	if _, err = pool.Exec(ctx, `UPDATE invite_reward_rules SET starts_at=$1`, clock.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	summary = call("GET", "/v1/invite", "", "", "", "", true)
	requireStatus(summary, 200)
	if data(summary)["rewards_enabled"] != false {
		t.Fatal("future rule advertised as active")
	}
}
