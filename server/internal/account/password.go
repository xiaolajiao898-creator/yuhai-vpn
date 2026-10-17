package account

// 账号 = 用户自己的 TRON 地址，密码自己设置。
// 忘记密码：从注册地址向官方地址转一笔精确金额的 TRX（1 TRX + 随机零头），链上确认后重设。
//
// 设计要点：
//   - 地址只存 HMAC 查找键（login_lookup）和加密副本（login_address_enc），不存明文。
//   - 每个找回挑战金额不同，别人即使同时为你的地址发起找回，也认领不了你的那笔转账。
//   - 转账必须晚于挑战创建时间，且每笔交易只能用一次（tx_id 唯一）。
//   - 重设成功后，旧设备全部下线、全部令牌作废（secret_version +1），只保留发起重设的这台。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/tron"
)

const (
	idemRegisterAddress = "register_address"
	resetChallengeTTL   = 30 * time.Minute
	resetBaseSun        = 1_000_000 // 1 TRX
	resetJitterSun      = 9_999     // 零头 0.000001–0.009999 TRX
	minPasswordRunes    = 8
	maxPasswordBytes    = 128
)

// ---------- 输入输出 ----------

type AddressRegisterInput struct {
	DeviceInfo
	Address  string      `json:"address"`
	Password string      `json:"password"`
	Invite   *InviteHint `json:"invite,omitempty"`
}

type AddressRegisterResult struct {
	AccountNo    string    `json:"account_no"`
	LoginAddress string    `json:"login_address"`
	InviteCode   string    `json:"invite_code"`
	InviteBound  bool      `json:"invite_bound"`
	DeviceID     string    `json:"device_id"`
	Tokens       Tokens    `json:"tokens"`
	ServerTime   time.Time `json:"server_time"`
}

type PasswordLoginInput struct {
	DeviceInfo
	Address  string `json:"address"`
	Password string `json:"password"`
}

type ResetStartInput struct {
	Address string `json:"address"`
}

type ResetStartResult struct {
	ChallengeID string    `json:"challenge_id"`
	PayTo       string    `json:"pay_to"`
	Amount      string    `json:"amount"` // 以 TRX 计，6 位小数，例如 "1.004372"
	AmountSun   int64     `json:"amount_sun"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ResetCompleteInput struct {
	DeviceInfo
	ChallengeID string `json:"challenge_id"`
	NewPassword string `json:"new_password"`
}

type ChangePasswordInput struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ---------- 地址与密码 ----------

func normalizeLoginAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !tron.ValidAddress(s) {
		return "", httpx.NewError(httpx.ErrBadRequest, "请填写正确的 USDT（TRC20）地址，以 T 开头，共 34 位")
	}
	return s, nil
}

func loginLookup(pepper []byte, address string) string {
	h := hmac.New(sha256.New, pepper)
	h.Write([]byte("login-address:v1:"))
	h.Write([]byte(address))
	return hex.EncodeToString(h.Sum(nil))
}

func validatePassword(p string) error {
	if !utf8.ValidString(p) || len(p) > maxPasswordBytes {
		return httpx.NewError(httpx.ErrBadRequest, "密码不能超过 128 个字符")
	}
	if utf8.RuneCountInString(p) < minPasswordRunes {
		return httpx.NewError(httpx.ErrBadRequest, "密码至少 8 位")
	}
	var letter, digit bool
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			letter = true
		}
	}
	if !letter || !digit {
		return httpx.NewError(httpx.ErrBadRequest, "密码需要同时包含字母和数字")
	}
	return nil
}

// Argon2id，参数取 OWASP 推荐的 19 MiB / 2 次 / 1 线程：单次约几十毫秒，2 核服务器可承受。
const pwPrefix = "$argon2id$v=19$m=19456,t=2,p=1$"

func hashPassword(p string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(p), salt, 2, 19456, 1, 32)
	return pwPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(sum), nil
}

// dummyHash 用于账号不存在时也做一次同等耗时的计算，避免用响应时间探测地址是否注册。
var dummyHash, _ = hashPassword("dummy-password-for-timing-1")

func verifyPassword(encoded, p string) bool {
	if len(p) > maxPasswordBytes || !strings.HasPrefix(encoded, pwPrefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, pwPrefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[0])
	want, e2 := base64.RawStdEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, 2, 19456, 1, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (s *Service) encryptAddress(userID int64, address string) ([]byte, error) {
	return s.Enc.Encrypt([]byte(address), fieldenc.AAD("users", "login_address_enc", userID))
}

// loginAddress 返回账户绑定的登录地址；旧账户（账户密钥方式）返回空串。
func (s *Service) loginAddress(ctx context.Context, userID int64) (string, error) {
	var blob []byte
	if err := s.Repo.Pool.QueryRow(ctx, `SELECT login_address_enc FROM users WHERE id=$1`, userID).Scan(&blob); err != nil {
		return "", err
	}
	if len(blob) == 0 {
		return "", nil
	}
	pt, err := s.Enc.Decrypt(blob, fieldenc.AAD("users", "login_address_enc", userID))
	if err != nil {
		return "", httpx.Internal.Wrap(err)
	}
	return string(pt), nil
}

// ---------- 注册 ----------

// RegisterWithAddress 用 TRON 地址 + 自设密码创建账户。幂等语义与 Register 相同。
func (s *Service) RegisterWithAddress(ctx context.Context, in AddressRegisterInput, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (AddressRegisterResult, int, error) {
	address, err := normalizeLoginAddress(in.Address)
	if err != nil {
		return AddressRegisterResult{}, 0, err
	}
	if err := validatePassword(in.Password); err != nil {
		return AddressRegisterResult{}, 0, err
	}
	if err := validateDevice(in.DeviceInfo); err != nil {
		return AddressRegisterResult{}, 0, err
	}
	pwHash, err := hashPassword(in.Password)
	if err != nil {
		return AddressRegisterResult{}, 0, err
	}
	lookup := loginLookup(s.Pepper, address)
	var result AddressRegisterResult
	for attempt := 0; attempt < 3; attempt++ {
		err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "register:"+idemKey); e != nil {
				return e
			}
			// 同一地址并发注册也串行化，确保「地址已注册」的判断可靠。
			if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "login:"+lookup); e != nil {
				return e
			}
			var e error
			result, e = s.registerAddressTx(ctx, tx, in, address, lookup, pwHash, idemKey, rawBody, ipHash, ipPrefixHash)
			return e
		})
		if !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return AddressRegisterResult{}, 0, err
	}
	return result, 201, nil
}

func (s *Service) registerAddressTx(ctx context.Context, tx pgx.Tx, in AddressRegisterInput, address, lookup, pwHash, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (AddressRegisterResult, error) {
	now := s.Clock.Now()
	reqHash := hashBytes(rawBody)
	var none AddressRegisterResult
	if prev, err := s.Repo.getIdem(ctx, tx, idemUserRegister, idemKey, idemRegisterAddress); err != nil {
		return none, err
	} else if prev != nil {
		if prev.RequestHash != reqHash {
			return none, httpx.NewError(httpx.ErrConflict, "幂等键冲突")
		}
		if now.Sub(prev.CreatedAt) > idemRegisterTTL || len(prev.Response) <= 2 {
			return none, httpx.NewError(httpx.ErrNotFound, "注册记录已过期，请直接登录")
		}
		var encrypted []byte
		if err := json.Unmarshal(prev.Response, &encrypted); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		pt, err := s.Enc.Decrypt(encrypted, fieldenc.AAD("api_idempotency", "response", idemKey))
		if err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		var res AddressRegisterResult
		if err := json.Unmarshal(pt, &res); err != nil {
			return none, httpx.Internal.Wrap(err)
		}
		return res, nil
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE login_lookup=$1)`, lookup).Scan(&taken); err != nil {
		return none, err
	}
	if taken {
		return none, httpx.NewError(httpx.ErrAddressTaken, "这个地址已经注册过了，请直接登录")
	}
	base, err := s.registerOnce(ctx, tx, RegisterInput{DeviceInfo: in.DeviceInfo, Invite: in.Invite}, now, ipHash, ipPrefixHash)
	if err != nil {
		return none, err
	}
	blob, err := s.encryptAddress(base.userID, address)
	if err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	// 没有账户密钥需要用户抄写，备份提醒直接视为已确认。
	if _, err := tx.Exec(ctx, `UPDATE users SET login_lookup=$2, login_address_enc=$3, password_hash=$4,
 password_changed_at=$5, backup_acknowledged_at=$5, updated_at=$5 WHERE id=$1`,
		base.userID, lookup, blob, pwHash, now); err != nil {
		return none, err
	}
	res := AddressRegisterResult{
		AccountNo: base.AccountNo, LoginAddress: address, InviteCode: base.InviteCode, InviteBound: base.InviteBound,
		DeviceID: base.DeviceID, Tokens: base.Tokens, ServerTime: now,
	}
	pt, _ := json.Marshal(res)
	enc, err := s.Enc.Encrypt(pt, fieldenc.AAD("api_idempotency", "response", idemKey))
	if err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	encoded, err := json.Marshal(enc)
	if err != nil {
		return none, err
	}
	if err := s.Repo.putIdem(ctx, tx, idemUserRegister, idemKey, idemRegisterAddress, reqHash, 201, encoded); err != nil {
		return none, httpx.Internal.Wrap(err)
	}
	return res, nil
}

// ---------- 登录 ----------

type loginRow struct {
	user   userRow
	pwHash string
}

func (s *Service) userByLogin(ctx context.Context, lookup string) (loginRow, error) {
	var r loginRow
	err := s.Repo.Pool.QueryRow(ctx, `
		SELECT id, account_no, invite_code, status, secret_version, backup_acknowledged_at, password_hash
		  FROM users WHERE login_lookup = $1`, lookup).
		Scan(&r.user.ID, &r.user.AccountNo, &r.user.InviteCode, &r.user.Status, &r.user.SecretVersion, &r.user.BackupAckAt, &r.pwHash)
	return r, err
}

// LoginWithPassword 地址 + 密码登录到当前设备。地址不存在与密码错误返回同一个错误。
func (s *Service) LoginWithPassword(ctx context.Context, in PasswordLoginInput, ipHash string) (RestoreResult, error) {
	now := s.Clock.Now()
	invalid := httpx.NewError(httpx.ErrLoginInvalid, "地址或密码错误")
	if err := validateDevice(in.DeviceInfo); err != nil {
		return RestoreResult{}, err
	}
	address, err := normalizeLoginAddress(in.Address)
	if err != nil {
		return RestoreResult{}, err
	}
	r, err := s.userByLogin(ctx, loginLookup(s.Pepper, address))
	if errors.Is(err, pgx.ErrNoRows) {
		verifyPassword(dummyHash, in.Password)
		return RestoreResult{}, invalid
	} else if err != nil {
		return RestoreResult{}, err
	}
	if !verifyPassword(r.pwHash, in.Password) {
		return RestoreResult{}, invalid
	}
	return s.signInUser(ctx, r.user, in.DeviceInfo, now, ipHash)
}

// ChangePassword 已登录用户修改密码（需要旧密码）。其他设备保持登录。
func (s *Service) ChangePassword(ctx context.Context, userID int64, in ChangePasswordInput) error {
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}
	var current *string
	if err := s.Repo.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&current); err != nil {
		return err
	}
	if current == nil {
		return httpx.NewError(httpx.ErrBadRequest, "这个账户使用账户密钥登录，没有设置密码")
	}
	if !verifyPassword(*current, in.OldPassword) {
		return httpx.NewError(httpx.ErrLoginInvalid, "原密码错误")
	}
	h, err := hashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	_, err = s.Repo.Pool.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=$3, updated_at=$3 WHERE id=$1`, userID, h, s.Clock.Now())
	return err
}

// ---------- 忘记密码 ----------

func (s *Service) resetEnabled() bool { return s.Tron != nil && tron.ValidAddress(s.ResetPayTo) }

func formatSun(sun int64) string {
	return fmt.Sprintf("%d.%06d", sun/1_000_000, sun%1_000_000)
}

// StartPasswordReset 为地址创建找回挑战，返回收款地址与精确金额。
func (s *Service) StartPasswordReset(ctx context.Context, in ResetStartInput) (ResetStartResult, error) {
	if !s.resetEnabled() {
		return ResetStartResult{}, httpx.NewError(httpx.ErrMaintenance, "找回密码暂未开放，请联系客服")
	}
	address, err := normalizeLoginAddress(in.Address)
	if err != nil {
		return ResetStartResult{}, err
	}
	now := s.Clock.Now()
	var res ResetStartResult
	err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		var userID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE login_lookup=$1 FOR NO KEY UPDATE`, loginLookup(s.Pepper, address)).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "这个地址还没有注册")
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE password_reset_challenges SET status='EXPIRED'
 WHERE user_id=$1 AND status='PENDING' AND expires_at < $2`, userID, now); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM password_reset_challenges WHERE user_id=$1 AND status='PENDING'`, userID).Scan(&pending); err != nil {
			return err
		}
		if pending >= 5 {
			return httpx.NewError(httpx.ErrRateLimited, "找回请求太多，请 30 分钟后再试")
		}
		for i := 0; i < 5; i++ {
			j, err := rand.Int(rand.Reader, big.NewInt(resetJitterSun))
			if err != nil {
				return err
			}
			amount := resetBaseSun + 1 + j.Int64()
			var id uuid.UUID
			expires := now.Add(resetChallengeTTL)
			err = tx.QueryRow(ctx, `INSERT INTO password_reset_challenges(user_id, pay_to, amount_sun, created_at, expires_at)
 VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING RETURNING id`, userID, s.ResetPayTo, amount, now, expires).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue // 同账户已有相同金额的进行中挑战，换一个零头
			}
			if err != nil {
				return err
			}
			res = ResetStartResult{ChallengeID: id.String(), PayTo: s.ResetPayTo, Amount: formatSun(amount), AmountSun: amount, ExpiresAt: expires}
			return nil
		}
		return httpx.NewError(httpx.ErrMaintenance, "请稍后再试")
	})
	return res, err
}

// CompletePasswordReset 链上确认转账后重设密码，并把发起设备登录。
// 还没查到转账时返回 ErrResetNotPaid，客户端隔几秒重试即可。
func (s *Service) CompletePasswordReset(ctx context.Context, in ResetCompleteInput, ipHash string) (RestoreResult, error) {
	if !s.resetEnabled() {
		return RestoreResult{}, httpx.NewError(httpx.ErrMaintenance, "找回密码暂未开放，请联系客服")
	}
	if err := validateDevice(in.DeviceInfo); err != nil {
		return RestoreResult{}, err
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return RestoreResult{}, err
	}
	cid, err := uuid.Parse(in.ChallengeID)
	if err != nil {
		return RestoreResult{}, httpx.NewError(httpx.ErrBadRequest, "challenge_id 无效")
	}
	now := s.Clock.Now()
	var userID, amount int64
	var payTo, status string
	var created, expires time.Time
	err = s.Repo.Pool.QueryRow(ctx, `SELECT user_id, pay_to, amount_sun, status, created_at, expires_at
 FROM password_reset_challenges WHERE id=$1`, cid).Scan(&userID, &payTo, &amount, &status, &created, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreResult{}, httpx.NewError(httpx.ErrNotFound, "找回请求不存在，请重新发起")
	} else if err != nil {
		return RestoreResult{}, err
	}
	switch {
	case status == "COMPLETED":
		return RestoreResult{}, httpx.NewError(httpx.ErrConflict, "这次找回已经完成，请用新密码登录")
	case status == "EXPIRED" || now.After(expires.Add(2*time.Hour)):
		// 过期后仍给 2 小时宽限：用户可能在最后一刻转账，链上确认需要时间。
		// 转账时间本身仍须晚于挑战创建时间，所以宽限不会放进旧交易。
		return RestoreResult{}, httpx.NewError(httpx.ErrNotFound, "找回请求已过期，请重新发起")
	}
	address, err := s.loginAddress(ctx, userID)
	if err != nil {
		return RestoreResult{}, err
	}
	if address == "" {
		return RestoreResult{}, httpx.NewError(httpx.ErrNotFound, "找回请求不存在，请重新发起")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	transfer, err := s.Tron.FindTRXTransfer(lookupCtx, address, payTo, amount, created)
	if errors.Is(err, tron.ErrTransferNotFound) {
		return RestoreResult{}, httpx.NewError(httpx.ErrResetNotPaid, "还没有查到这笔转账，确认一般需要 1 分钟左右").
			WithDetails(map[string]any{"pay_to": payTo, "amount": formatSun(amount)})
	} else if err != nil {
		return RestoreResult{}, httpx.NewError(httpx.ErrMaintenance, "链上查询暂时不可用，请稍后再试").Wrap(err)
	}
	pwHash, err := hashPassword(in.NewPassword)
	if err != nil {
		return RestoreResult{}, err
	}
	var res RestoreResult
	err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		var ustatus, accountNo string
		if err := tx.QueryRow(ctx, `SELECT status, account_no FROM users WHERE id=$1 FOR NO KEY UPDATE`, userID).Scan(&ustatus, &accountNo); err != nil {
			return err
		}
		if ustatus == "BANNED" {
			return httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
		}
		tag, err := tx.Exec(ctx, `UPDATE password_reset_challenges SET status='COMPLETED', tx_id=$2, completed_at=$3
 WHERE id=$1 AND status IN ('PENDING','EXPIRED')`, cid, transfer.TxID, now)
		if isUniqueViolation(err) {
			return httpx.NewError(httpx.ErrConflict, "这笔转账已经用过了，请重新发起找回")
		} else if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewError(httpx.ErrConflict, "这次找回已经完成，请用新密码登录")
		}
		// 其余进行中的挑战一并作废。
		if _, err := tx.Exec(ctx, `UPDATE password_reset_challenges SET status='EXPIRED' WHERE user_id=$1 AND status='PENDING'`, userID); err != nil {
			return err
		}
		active, err := activeDeviceIDs(ctx, tx, userID)
		if err != nil {
			return err
		}
		for _, id := range active {
			if err := s.Repo.lockDeviceSessions(ctx, tx, id); err != nil {
				return err
			}
		}
		for _, id := range active {
			if _, err := s.Repo.revokeDevice(ctx, tx, userID, id, now); err != nil {
				return err
			}
			if err := s.Repo.insertBindLog(ctx, tx, userID, id, "REVOKE_BY_SECRET_RESET", "USER", ipHash,
				"password-reset:"+cid.String()+":"+strconv.FormatInt(id, 10)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL`, userID, now); err != nil {
			return err
		}
		var version int
		if err := tx.QueryRow(ctx, `UPDATE users SET password_hash=$2, password_changed_at=$3, secret_version=secret_version+1, updated_at=$3
 WHERE id=$1 RETURNING secret_version`, userID, pwHash, now).Scan(&version); err != nil {
			return err
		}
		deviceID, err := s.newDevice(ctx, tx, userID, in.DeviceInfo)
		if err != nil {
			if isUniqueViolation(err) {
				return httpx.NewError(httpx.ErrConflict, "设备标识已存在，请重新生成")
			}
			return err
		}
		if err := s.Repo.insertBindLog(ctx, tx, userID, deviceID, "RESTORE", "USER", ipHash, "password-reset:"+cid.String()); err != nil {
			return err
		}
		tokens, err := s.issueTokens(ctx, tx, userID, deviceID, in.DevicePubKey, version, now)
		if err != nil {
			return err
		}
		res = RestoreResult{AccountNo: accountNo, DeviceID: strconv.FormatInt(deviceID, 10), Tokens: tokens, ServerTime: now}
		return nil
	})
	return res, err
}

// ---------- HTTP ----------

func (h *Handler) mountPassword(v1 *gin.RouterGroup) {
	ip := func(c *gin.Context) string { return auth.ClientIP(c) }
	prefix := func(c *gin.Context) string { return ipPrefix(auth.ClientIP(c)) }
	user := func(c *gin.Context) string { return itoa64(httpx.UserID(c)) }
	a := v1.Group("/auth")
	a.POST("/address/register",
		h.MW.RateLimit("register_ip", 5, time.Hour, true, ip),
		h.MW.RateLimit("register_prefix", 30, time.Hour, true, prefix),
		h.registerAddress)
	a.POST("/address/login", h.MW.RateLimit("login_ip", 30, time.Hour, true, ip), h.loginPassword)
	a.POST("/password-reset/start", h.MW.RateLimit("reset_start_ip", 10, time.Hour, true, ip), h.resetStart)
	a.POST("/password-reset/complete", h.MW.RateLimit("reset_complete_ip", 240, time.Hour, true, ip), h.resetComplete)
	v1.POST("/account/password", h.MW.S2(), h.MW.RateLimit("password_change_user", 10, time.Hour, true, user), h.changePassword)
}

func readJSON(c *gin.Context, out any) ([]byte, bool) {
	body, err := auth.ReadBody(c)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "请求体过大"))
		return nil, false
	}
	if err := json.Unmarshal(body, out); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "参数格式错误"))
		return nil, false
	}
	return body, true
}

func (h *Handler) registerAddress(c *gin.Context) {
	var in AddressRegisterInput
	body, ok := readJSON(c, &in)
	if !ok {
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	idem := c.GetHeader("Idempotency-Key")
	if _, err := uuid.Parse(idem); err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "Idempotency-Key 必须是 UUID"))
		return
	}
	if c.GetHeader(auth.HeaderDeviceID) != in.DeviceUID {
		httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "X-Device-Id 与 device_uid 不一致"))
		return
	}
	ipHash, prefixHash := hashIP(auth.ClientIP(c))
	res, status, err := h.Svc.RegisterWithAddress(c.Request.Context(), in, idem, body, ipHash, prefixHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(status, gin.H{"data": res, "request_id": httpx.RequestID(c)})
}

// addressLimit 按地址限流（地址先规范化，否则大小写/空格变体能绕过）。Redis 不可用时拒绝。
func (h *Handler) addressLimit(c *gin.Context, name, address string, limit int, window time.Duration) bool {
	a, err := normalizeLoginAddress(address)
	if err != nil {
		return true // 格式错误交给业务层返回明确提示
	}
	ok, retry, err := h.MW.Redis.Allow(c.Request.Context(), name+":"+loginLookup(h.Svc.Pepper, a), limit, window)
	if err != nil {
		httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试"))
		return false
	}
	if !ok {
		c.Header("Retry-After", itoaDur(retry))
		httpx.Fail(c, httpx.NewError(httpx.ErrRateLimited, "尝试次数过多，请稍后再试"))
		return false
	}
	return true
}

func (h *Handler) loginPassword(c *gin.Context) {
	var in PasswordLoginInput
	body, ok := readJSON(c, &in)
	if !ok {
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	if !h.addressLimit(c, "login_addr", in.Address, 10, time.Hour) {
		return
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	res, err := h.Svc.LoginWithPassword(c.Request.Context(), in, ipHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

func (h *Handler) resetStart(c *gin.Context) {
	var in ResetStartInput
	if _, ok := readJSON(c, &in); !ok {
		return
	}
	if !h.addressLimit(c, "reset_start_addr", in.Address, 6, time.Hour) {
		return
	}
	res, err := h.Svc.StartPasswordReset(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

func (h *Handler) resetComplete(c *gin.Context) {
	var in ResetCompleteInput
	body, ok := readJSON(c, &in)
	if !ok {
		return
	}
	if err := h.MW.VerifySelfSigned(c, in.DevicePubKey, body); err != nil {
		httpx.Fail(c, err)
		return
	}
	if cid, err := uuid.Parse(in.ChallengeID); err == nil {
		if ok, retry, err := h.MW.Redis.Allow(c.Request.Context(), "reset_complete_cid:"+cid.String(), 90, time.Hour); err != nil {
			httpx.Fail(c, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试"))
			return
		} else if !ok {
			c.Header("Retry-After", itoaDur(retry))
			httpx.Fail(c, httpx.NewError(httpx.ErrRateLimited, "查询太频繁，请稍后再试"))
			return
		}
	}
	ipHash, _ := hashIP(auth.ClientIP(c))
	res, err := h.Svc.CompletePasswordReset(c.Request.Context(), in, ipHash)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, res)
}

func (h *Handler) changePassword(c *gin.Context) {
	var in ChangePasswordInput
	if _, ok := readJSON(c, &in); !ok {
		return
	}
	if err := h.Svc.ChangePassword(c.Request.Context(), httpx.UserID(c), in); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"changed": true})
}
