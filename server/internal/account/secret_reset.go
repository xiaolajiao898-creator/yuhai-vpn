package account

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
)

// 账户密钥重置（app.yaml POST /v1/account/secret/reset）。
//
// 这个匿名账户体系里，账户密钥就是全部凭证——没有手机号、没有邮箱、没有密码。
// 所以「重置密钥」不是改个字段，而是换锁，而换锁这件事在库里早就铺好了路：
//
//   - `users.secret_version` 从第 00002 号迁移起就在，访问令牌签发时把它写进
//     claims，鉴权中间件每个请求都比一次（internal/auth/middleware.go）。
//     版本号一涨，所有旧访问令牌当场失效，不需要令牌黑名单。
//   - `attachDevice` 与 `Refresh` 同样比对版本号，命中就回 40108 要求用新密钥恢复。
//   - `device_bind_logs.action` 的 CHECK 里早就列着 `REVOKE_BY_SECRET_RESET`。
//
// 缺的一直只是**写的那一端**。本文件补上，并且不绕开上面任何一条既有约定。
const (
	idemSecretReset = "secret_reset"
	// 新密钥的明文只在幂等记录里以整体加密的形式短暂存在，超时后由
	// Jobs.PurgeAccountSecrets 清空。与注册保持同一个展示窗口。
	idemSecretResetTTL = 10 * time.Minute
)

// SecretResetResult 与契约的 200 响应一一对应。account_secret 是**唯一一次**
// 出现明文新密钥的地方：库里只留 HMAC，事后谁都查不回来。
type SecretResetResult struct {
	AccountSecret string    `json:"account_secret"`
	Tokens        Tokens    `json:"tokens"`
	ServerTime    time.Time `json:"server_time"`
}

func (h *Handler) resetSecret(c *gin.Context) {
	// S2 中间件已经读过一次请求体做签名校验，这里拿的是同一份缓存。
	// 请求体参与幂等请求哈希：本接口没有请求体，于是哈希固定，
	// 同一个键配上任何非空请求体都会被判成冲突。
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return
	}
	idem := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(idem); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	res, err := h.Svc.ResetSecret(c.Request.Context(), httpx.UserID(c), httpx.DeviceID(c), idem, body, ipHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

// ResetSecret 换发账户密钥。幂等：同一 Idempotency-Key 在 10 分钟内重放返回**同一把**
// 新密钥，不会二次轮换；超窗返回 40401（明确告诉调用方那把密钥再也拿不回来了）。
func (s *Service) ResetSecret(ctx context.Context, userID, deviceID int64, idemKey string,
	rawBody []byte, ipHash string) (SecretResetResult, error) {
	if userID <= 0 || deviceID <= 0 {
		return SecretResetResult{}, httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
	}
	var out SecretResetResult
	var err error
	// secret_lookup 有唯一约束。100 bit 熵撞上的概率可以忽略，但注册那条路径
	// 就是这么重试的；这里不搞第二套规矩。
	for attempt := 0; attempt < 3; attempt++ {
		err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
			// 同一幂等键串行：并发重放里只有一个事务会真的轮换密钥。
			if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
				"secret-reset:"+idemKey); e != nil {
				return e
			}
			var e error
			out, e = s.resetSecretTx(ctx, tx, userID, deviceID, idemKey, rawBody, ipHash)
			return e
		})
		if !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return SecretResetResult{}, err
	}
	return out, nil
}

func (s *Service) resetSecretTx(ctx context.Context, tx pgx.Tx, userID, deviceID int64,
	idemKey string, rawBody []byte, ipHash string) (SecretResetResult, error) {
	now := s.Clock.Now()
	reqHash := hashBytes(rawBody)
	var none SecretResetResult

	// api_idempotency 的唯一键是 (user_id, idem_key)，**不含 endpoint**，
	// 而 putIdem 是 ON CONFLICT DO NOTHING。所以只按 endpoint 过滤地查一遍
	// 是不够的：若这个键已经被别的接口占掉，我们会以为是新请求，真的换一次锁，
	// 然后把响应静静地丢掉——重放时再换一次。这里按裸键查，占用即冲突。
	var endpoint, prevHash string
	var prevResp []byte
	var prevAt time.Time
	e := tx.QueryRow(ctx, `SELECT endpoint, request_hash, COALESCE(response, '{}'), created_at
 FROM api_idempotency WHERE user_id=$1 AND idem_key=$2`, userID, idemKey).
		Scan(&endpoint, &prevHash, &prevResp, &prevAt)
	switch {
	case e == nil:
		if endpoint != idemSecretReset || prevHash != reqHash {
			return none, httpx.NewError(httpx.ErrConflict, "幂等键冲突")
		}
		if now.Sub(prevAt) > idemSecretResetTTL || len(prevResp) <= 2 {
			// 明文新密钥只在这条记录里存在过，清掉之后服务端也拿不回来。
			// 这里绝不能顺手再重置一次：那会把幂等键变成「每次调用都换一把密钥」，
			// 正好和幂等要防的事相反。
			return none, httpx.NewError(httpx.ErrNotFound,
				"上次重置的新密钥已超过展示期限，无法再次取回；请用新的 Idempotency-Key 重新发起重置")
		}
		var encrypted []byte
		if err := json.Unmarshal(prevResp, &encrypted); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		pt, err := s.Enc.Decrypt(encrypted, fieldenc.AAD("api_idempotency", "response", idemKey))
		if err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		var res SecretResetResult
		if err := json.Unmarshal(pt, &res); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		return res, nil
	case !errors.Is(e, pgx.ErrNoRows):
		return none, e
	}

	// 账户行锁，与恢复、替换设备、移除设备共用同一把（FOR NO KEY UPDATE，
	// 与会话/设备插入的 KEY SHARE 兼容）。拿住它，重置期间不会有另一个事务
	// 把新设备挂上来，也不会有第二次重置同时进行。
	var status string
	e = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR NO KEY UPDATE`, userID).Scan(&status)
	if errors.Is(e, pgx.ErrNoRows) {
		return none, httpx.NewError(httpx.ErrTokenMissing, "访问令牌无效")
	}
	if e != nil {
		return none, e
	}
	if status == "BANNED" {
		return none, httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
	}

	// 发起设备必须仍然有效。否则一台刚被移除的设备可以靠尚未过期的访问令牌
	// 发起重置，把自己变成唯一留下的设备，还顺手把真正的主人踢下线——
	// 这正是被盗设备最想要的结果。
	var current string
	e = tx.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 AND user_id=$2 FOR SHARE`,
		deviceID, userID).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) || (e == nil && current != "ACTIVE") {
		return none, httpx.NewError(httpx.ErrDeviceRevoked, "此设备已被移除")
	}
	if e != nil {
		return none, e
	}

	active, e := activeDeviceIDs(ctx, tx, userID)
	if e != nil {
		return none, e
	}
	// 先按 id 升序把全部在线设备的刷新栅栏一次拿齐，再动手。
	// 逐台「锁一台撤一台」会让并发刷新在两次撤销之间提交出新的后代令牌，
	// 而下面那条 UPDATE 的快照看不到它。升序是为了和别的路径统一加锁顺序。
	for _, id := range active {
		if err := s.Repo.lockDeviceSessions(ctx, tx, id); err != nil {
			return none, err
		}
	}
	for _, id := range active {
		if id == deviceID {
			continue // 发起设备保留：点一次重置不该把用户自己锁在门外。
		}
		if _, err := s.Repo.revokeDevice(ctx, tx, userID, id, now); err != nil {
			return none, err
		}
		if err := s.Repo.insertBindLog(ctx, tx, userID, id, "REVOKE_BY_SECRET_RESET", "USER", ipHash,
			"secret-reset:"+idemKey+":"+strconv.FormatInt(id, 10)); err != nil {
			return none, err
		}
	}
	// 连发起设备的会话一起作废——契约说的是「全部令牌失效」。
	// 新令牌在这条 UPDATE 之后才插入，因此不会被自己扫掉。
	if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2
 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now); err != nil {
		return none, err
	}

	secret, err := ids.NewAccountSecret()
	if err != nil {
		return none, err
	}
	// backup_acknowledged_at 一并复位：新密钥用户还没抄下来，
	// GET /v1/me 的备份提醒必须重新亮起来，否则等于替用户签了字。
	var version int
	if err := tx.QueryRow(ctx, `UPDATE users
 SET secret_lookup=$2, secret_version=secret_version+1, backup_acknowledged_at=NULL, updated_at=$3
 WHERE id=$1 RETURNING secret_version`,
		userID, ids.SecretLookup(s.Pepper, secret), now).Scan(&version); err != nil {
		return none, err
	}

	p, err := s.Repo.lookupDevice(ctx, tx, deviceID)
	if err != nil {
		return none, err
	}
	tokens, err := s.issueTokens(ctx, tx, userID, deviceID, p.PubKeySPKI, version, now)
	if err != nil {
		return none, err
	}
	res := SecretResetResult{AccountSecret: secret, Tokens: tokens, ServerTime: now}

	pt, err := json.Marshal(res)
	if err != nil {
		return none, err
	}
	enc, err := s.Enc.Encrypt(pt, fieldenc.AAD("api_idempotency", "response", idemKey))
	if err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	encoded, err := json.Marshal(enc)
	if err != nil {
		return none, err
	}
	if err := s.Repo.putIdem(ctx, tx, userID, idemKey, idemSecretReset, reqHash, 200, encoded); err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	return res, nil
}

func activeDeviceIDs(ctx context.Context, tx pgx.Tx, userID int64) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM devices WHERE user_id=$1 AND status='ACTIVE' ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
