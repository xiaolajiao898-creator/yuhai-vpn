package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/ids"
	"github.com/yuhai-vpn/server/internal/invite"
	"github.com/yuhai-vpn/server/internal/money"
	"github.com/yuhai-vpn/server/internal/redisx"
	"github.com/yuhai-vpn/server/internal/tron"
)

const (
	idemUserRegister = 0 // 注册时还没有 user_id，幂等记录挂在 0 下
	idemRegisterTTL  = 10 * time.Minute
	restoreTicketTTL = 5 * time.Minute
	noticeWindow     = 7 * 24 * time.Hour
)

type Service struct {
	Repo   *Repo
	Signer *auth.Signer
	Enc    *fieldenc.Cipher
	Redis  *redisx.Client
	Clock  clock.Clock
	Pepper []byte
	// 忘记密码：链上查询与官方收款地址。任一为空时重置功能关闭（返回维护中），不影响注册登录。
	Tron       tron.TransferFinder
	ResetPayTo string
	// WireGuard 隧道地址池，来自 config；为空时 PUT /v1/devices/current/wg-key 直接拒绝，
	// 不会分配出一个没有配置依据的地址。
	WGPool     *net.IPNet
	WGReserved int
}

// ---------- 注册 ----------

// Register 创建匿名账户。幂等：同一 Idempotency-Key + 相同请求体 10 分钟内返回首次结果（含账户密钥，加密保存）；
// 超过 10 分钟返回 40401 引导恢复账户；请求体不同返回 40901。
func (s *Service) Register(ctx context.Context, in RegisterInput, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (RegisterResult, int, error) {
	var result RegisterResult
	var status int
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
			// 同一幂等键串行处理；事务提交时账户和加密响应同时可见。
			if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "register:"+idemKey); e != nil {
				return e
			}
			var e error
			result, status, e = s.registerTx(ctx, tx, in, idemKey, rawBody, ipHash, ipPrefixHash)
			return e
		})
		if !isUniqueViolation(err) {
			break
		}
	}
	return result, status, err
}

func (s *Service) registerTx(ctx context.Context, tx pgx.Tx, in RegisterInput, idemKey string, rawBody []byte, ipHash, ipPrefixHash string) (RegisterResult, int, error) {
	now := s.Clock.Now()
	reqHash := hashBytes(rawBody)
	if prev, err := s.Repo.getIdem(ctx, tx, idemUserRegister, idemKey, "register"); err != nil {
		return RegisterResult{}, 0, err
	} else if prev != nil {
		if prev.RequestHash != reqHash {
			return RegisterResult{}, 0, httpx.NewError(httpx.ErrConflict, "幂等键冲突")
		}
		if now.Sub(prev.CreatedAt) > idemRegisterTTL || len(prev.Response) <= 2 {
			return RegisterResult{}, 0, httpx.NewError(httpx.ErrNotFound, "注册记录已过期，请使用账户密钥恢复")
		}
		var encrypted []byte
		if err := json.Unmarshal(prev.Response, &encrypted); err != nil {
			return RegisterResult{}, 0, httpx.Internal.Wrap(err)
		}
		pt, err := s.Enc.Decrypt(encrypted, fieldenc.AAD("api_idempotency", "response", idemKey))
		if err != nil {
			return RegisterResult{}, 0, httpx.Internal.Wrap(err)
		}
		var res RegisterResult
		if err := json.Unmarshal(pt, &res); err != nil {
			return RegisterResult{}, 0, httpx.Internal.Wrap(err)
		}
		return res, 201, nil
	}
	if err := validateDevice(in.DeviceInfo); err != nil {
		return RegisterResult{}, 0, err
	}

	res, err := s.registerOnce(ctx, tx, in, now, ipHash, ipPrefixHash)
	if err != nil {
		return RegisterResult{}, 0, err
	}
	// 幂等记录：整个响应用信封加密（含账户密钥），10 分钟后由任务清空 response
	pt, _ := json.Marshal(res)
	enc, err := s.Enc.Encrypt(pt, fieldenc.AAD("api_idempotency", "response", idemKey))
	if err != nil {
		return RegisterResult{}, 0, httpx.Internal.Wrap(err)
	}
	encoded, err := json.Marshal(enc)
	if err != nil {
		return RegisterResult{}, 0, err
	}
	if err := s.Repo.putIdem(ctx, tx, idemUserRegister, idemKey, "register", reqHash, 201, encoded); err != nil {
		return RegisterResult{}, 0, httpx.Internal.Wrap(err)
	}
	return res, 201, nil
}

func (s *Service) registerOnce(ctx context.Context, tx pgx.Tx, in RegisterInput, now time.Time, ipHash, ipPrefixHash string) (RegisterResult, error) {
	accountNo, err := ids.NewAccountNo()
	if err != nil {
		return RegisterResult{}, err
	}
	secret, err := ids.NewAccountSecret()
	if err != nil {
		return RegisterResult{}, err
	}
	inviteCode, err := ids.NewInviteCode()
	if err != nil {
		return RegisterResult{}, err
	}
	lookup := ids.SecretLookup(s.Pepper, secret)
	channel := ""
	if in.Invite != nil {
		channel = in.Invite.Method
	}

	var res RegisterResult
	err = func() error {
		userID, err := s.Repo.insertUser(ctx, tx, accountNo, lookup, inviteCode, channel, ipHash, ipPrefixHash)
		if err != nil {
			return err
		}
		if err := s.Repo.initEntitlement(ctx, tx, userID); err != nil {
			return err
		}
		deviceID, err := s.newDevice(ctx, tx, userID, in.DeviceInfo)
		if err != nil {
			if isUniqueViolation(err) { // device_uid 已存在：客户端应重新生成设备标识
				return httpx.NewError(httpx.ErrConflict, "设备标识已存在，请重新生成")
			}
			return err
		}
		if err := s.Repo.insertBindLog(ctx, tx, userID, deviceID, "REGISTER", "USER", ipHash, "register:"+in.DeviceUID); err != nil {
			return err
		}
		bound := false
		if in.Invite != nil && in.Invite.Code != "" {
			// 邀请可选：业务拒绝仅回滚保存点，不能吞掉 SQL 错误后继续
			// 使用 aborted 的注册事务；基础设施失败仍回滚整个注册。
			bindingTx, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			_, bindErr := invite.BindInTx(ctx, bindingTx, userID, invite.BindInput{Code: in.Invite.Code, Method: bindMethod(in.Invite.Method)}, s.Clock)
			if bindErr == nil {
				if err := bindingTx.Commit(ctx); err != nil {
					return err
				}
				bound = true
			} else {
				if err := bindingTx.Rollback(ctx); err != nil {
					return err
				}
				var apiErr *httpx.Error
				if !errors.As(bindErr, &apiErr) || apiErr.Code.HTTPStatus() >= 500 {
					return bindErr
				}
			}
		}
		tokens, err := s.issueTokens(ctx, tx, userID, deviceID, in.DevicePubKey, 1, now)
		if err != nil {
			return err
		}
		res = RegisterResult{
			AccountNo: accountNo, AccountSecret: secret, InviteCode: inviteCode, InviteBound: bound,
			DeviceID: strconv.FormatInt(deviceID, 10), Tokens: tokens, ServerTime: now, userID: userID,
		}
		return nil
	}()
	return res, err
}

// ---------- 恢复 ----------

// Restore 用账户密钥在新设备登录。密钥错误统一 40104；设备满返回 40902 + restore_ticket。
func (s *Service) Restore(ctx context.Context, in RestoreInput, ipHash string) (RestoreResult, error) {
	now := s.Clock.Now()
	if err := validateDevice(in.DeviceInfo); err != nil {
		return RestoreResult{}, err
	}
	invalid := httpx.NewError(httpx.ErrSecretInvalid, "账户密钥无效")
	normalized, err := ids.NormalizeSecret(in.AccountSecret)
	if err != nil {
		return RestoreResult{}, invalid
	}
	u, err := s.Repo.userBySecretLookup(ctx, ids.SecretLookup(s.Pepper, normalized))
	if errors.Is(err, pgx.ErrNoRows) {
		return RestoreResult{}, invalid
	} else if err != nil {
		return RestoreResult{}, err
	}
	return s.signInUser(ctx, u, in.DeviceInfo, now, ipHash)
}

// signInUser 把已验证身份的账户登录到新设备：设备满时返回 40902 + restore_ticket，
// 客户端用 replace-device 移除一台后继续。账户密钥恢复与地址密码登录共用。
func (s *Service) signInUser(ctx context.Context, u userRow, d DeviceInfo, now time.Time, ipHash string) (RestoreResult, error) {
	if u.Status == "BANNED" {
		return RestoreResult{}, httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
	}
	limit, err := s.Repo.deviceLimit(ctx, s.Repo.Pool, u.ID, now)
	if err != nil {
		return RestoreResult{}, err
	}
	count, err := s.Repo.countActiveDevices(ctx, s.Repo.Pool, u.ID)
	if err != nil {
		return RestoreResult{}, err
	}
	if count >= limit {
		devices, err := s.Repo.listDevices(ctx, u.ID)
		if err != nil {
			return RestoreResult{}, err
		}
		ticket, err := s.Redis.PutTicket(ctx, "restore", strconv.FormatInt(u.ID, 10), restoreTicketTTL)
		if err != nil {
			return RestoreResult{}, httpx.NewError(httpx.ErrMaintenance, "系统繁忙，请稍后再试").Wrap(err)
		}
		return RestoreResult{}, httpx.NewError(httpx.ErrDeviceLimit, "设备数量已达上限").
			WithDetails(map[string]any{"device_limit": limit, "restore_ticket": ticket, "devices": devices})
	}
	return s.attachDevice(ctx, u, d, now, ipHash, 0)
}

// ReplaceDevice 恢复时设备已满：用票据移除一台后继续。
func (s *Service) ReplaceDevice(ctx context.Context, in ReplaceDeviceInput, ipHash string) (RestoreResult, error) {
	now := s.Clock.Now()
	if err := validateDevice(in.DeviceInfo); err != nil {
		return RestoreResult{}, err
	}
	payload, err := s.Redis.TakeTicket(ctx, "restore", in.RestoreTicket)
	if errors.Is(err, redisx.ErrTicketNotFound) {
		return RestoreResult{}, httpx.NewError(httpx.ErrSecretInvalid, "票据无效或已过期，请重新输入账户密钥")
	} else if err != nil {
		return RestoreResult{}, err
	}
	userID, _ := strconv.ParseInt(payload, 10, 64)
	u, err := s.Repo.userByID(ctx, userID)
	if err != nil {
		return RestoreResult{}, err
	}
	removeID, err := strconv.ParseInt(in.RemoveDeviceID, 10, 64)
	if err != nil {
		return RestoreResult{}, httpx.NewError(httpx.ErrBadRequest, "remove_device_id 无效")
	}
	return s.attachDevice(ctx, u, in.DeviceInfo, now, ipHash, removeID)
}

func (s *Service) attachDevice(ctx context.Context, u userRow, d DeviceInfo, now time.Time, ipHash string, removeID int64) (RestoreResult, error) {
	var res RestoreResult
	err := db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		// 恢复与替换设备共用同一账户锁。只在 READ COMMITTED 事务中数设备
		// 无法阻止并发插入；必须把核对上限、移除旧设备和绑定新设备串行执行。
		// NO KEY UPDATE 与会话/设备插入的外键 KEY SHARE 兼容，避免与刷新
		// 令牌事务形成“账户锁 ↔ 会话锁”的循环等待。
		var status string
		var version int
		if err := tx.QueryRow(ctx, `SELECT status, secret_version FROM users WHERE id = $1 FOR NO KEY UPDATE`, u.ID).Scan(&status, &version); err != nil {
			return err
		}
		if status == "BANNED" {
			return httpx.NewError(httpx.ErrAccountDisabled, "账户已停用")
		}
		if version != u.SecretVersion {
			return httpx.NewError(httpx.ErrSecretRotated, "账户密钥已重置，请用新密钥恢复")
		}
		if removeID != 0 {
			ok, err := s.Repo.revokeDevice(ctx, tx, u.ID, removeID, now)
			if err != nil {
				return err
			}
			if !ok {
				return httpx.NewError(httpx.ErrNotFound, "要移除的设备不存在")
			}
			if err := s.Repo.insertBindLog(ctx, tx, u.ID, removeID, "REVOKE_BY_USER", "USER", ipHash, "revoke:"+strconv.FormatInt(removeID, 10)); err != nil {
				return err
			}
		}
		// 事务内再次核对上限，防止并发恢复超额
		limit, err := s.Repo.deviceLimit(ctx, tx, u.ID, now)
		if err != nil {
			return err
		}
		count, err := s.Repo.countActiveDevices(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		if count >= limit {
			return httpx.NewError(httpx.ErrDeviceLimit, "设备数量已达上限")
		}
		deviceID, err := s.newDevice(ctx, tx, u.ID, d)
		if err != nil {
			if isUniqueViolation(err) {
				return httpx.NewError(httpx.ErrConflict, "设备标识已存在，请重新生成")
			}
			return err
		}
		if err := s.Repo.insertBindLog(ctx, tx, u.ID, deviceID, "RESTORE", "USER", ipHash, "restore:"+d.DeviceUID); err != nil {
			return err
		}
		tokens, err := s.issueTokens(ctx, tx, u.ID, deviceID, d.DevicePubKey, u.SecretVersion, now)
		if err != nil {
			return err
		}
		res = RestoreResult{AccountNo: u.AccountNo, DeviceID: strconv.FormatInt(deviceID, 10), Tokens: tokens, ServerTime: now}
		return nil
	})
	return res, err
}

// ---------- 令牌 ----------

// Refresh 轮换刷新令牌。旧令牌再次出现 → 整族作废（40103）。调用前中间件已验证设备签名与（可过期的）访问令牌。
func (s *Service) Refresh(ctx context.Context, userID, deviceID int64, refreshToken string) (Tokens, error) {
	now := s.Clock.Now()
	replay := httpx.NewError(httpx.ErrRefreshReplay, "登录状态已失效，请重新恢复账户")
	var out Tokens
	var replayed bool
	err := db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		// 同设备的轮换与重放吊销串行执行，防止吊销旧令牌家族时，另一个
		// 请求恰好插入 UPDATE 快照之后的新后代令牌而逃过吊销。
		if err := s.Repo.lockDeviceSessions(ctx, tx, deviceID); err != nil {
			return err
		}
		sess, err := s.Repo.sessionByHashForUpdate(ctx, tx, auth.HashRefreshToken(refreshToken))
		if errors.Is(err, pgx.ErrNoRows) {
			return replay
		} else if err != nil {
			return err
		}
		if sess.UserID != userID || sess.DeviceID != deviceID {
			return replay
		}
		if sess.RotatedAt != nil { // 已轮换的旧令牌重放：视为泄露，整族作废
			if err := s.Repo.revokeFamily(ctx, tx, sess.FamilyID, now); err != nil {
				return err
			}
			replayed = true
			return nil // 先提交吊销；事务外再返回业务错误，不能触发回滚。
		}
		if sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
			return replay
		}
		p, err := s.Repo.lookupDevice(ctx, tx, deviceID)
		if err != nil || !p.DeviceActive {
			return httpx.NewError(httpx.ErrDeviceRevoked, "此设备已被移除")
		}
		if p.SecretVersion != sess.SecretVersion {
			return httpx.NewError(httpx.ErrSecretRotated, "账户密钥已重置，请用新密钥恢复")
		}
		if err := s.Repo.markRotated(ctx, tx, sess.ID, now); err != nil {
			return err
		}
		out, err = s.issueTokensInFamily(ctx, tx, userID, deviceID, p.PubKeySPKI, p.SecretVersion, sess.FamilyID, now)
		return err
	})
	if err == nil && replayed {
		return Tokens{}, replay
	}
	if err == nil {
		s.Repo.touchDevice(ctx, deviceID, now)
	}
	return out, err
}

func (s *Service) issueTokens(ctx context.Context, tx pgx.Tx, userID, deviceID int64, pubkey string, secretVersion int, now time.Time) (Tokens, error) {
	return s.issueTokensInFamily(ctx, tx, userID, deviceID, pubkey, secretVersion, uuid.NewString(), now)
}

func (s *Service) issueTokensInFamily(ctx context.Context, tx pgx.Tx, userID, deviceID int64, pubkey string, secretVersion int, familyID string, now time.Time) (Tokens, error) {
	access, err := s.Signer.Sign(auth.AccessClaims{
		Subject: userID, DeviceID: deviceID, SecretVersion: secretVersion, KeyThumbprint: auth.Thumbprint(pubkey),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(auth.AccessTokenTTL).Unix(),
	})
	if err != nil {
		return Tokens{}, err
	}
	refresh, hash, err := auth.NewRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	refreshExp := now.Add(auth.RefreshTokenTTL)
	if err := s.Repo.insertSession(ctx, tx, userID, deviceID, hash, familyID, secretVersion, refreshExp); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, AccessExpiresAt: now.Add(auth.AccessTokenTTL), RefreshToken: refresh, RefreshExpiresAt: refreshExp}, nil
}

// Logout 退出当前设备：设备记录移除、凭证吊销、会话作废。
func (s *Service) Logout(ctx context.Context, userID, deviceID int64, ipHash string) error {
	now := s.Clock.Now()
	return db.InTx(ctx, s.Repo.Pool, func(tx pgx.Tx) error {
		if _, err := s.Repo.revokeDevice(ctx, tx, userID, deviceID, now); err != nil {
			return err
		}
		return s.Repo.insertBindLog(ctx, tx, userID, deviceID, "REVOKE_BY_USER", "USER", ipHash, "revoke:"+strconv.FormatInt(deviceID, 10))
	})
}

func (s *Service) AckBackup(ctx context.Context, userID int64) error {
	return s.Repo.ackBackup(ctx, userID, s.Clock.Now())
}

// ---------- 首页聚合 ----------

func (s *Service) Me(ctx context.Context, userID, deviceID int64) (Me, error) {
	now := s.Clock.Now()
	u, err := s.Repo.userByID(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	e, err := s.Repo.entitlement(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	packs, err := s.Repo.activePacks(ctx, userID, now)
	if err != nil {
		return Me{}, err
	}
	limit, err := s.Repo.deviceLimit(ctx, s.Repo.Pool, userID, now)
	if err != nil {
		return Me{}, err
	}
	count, err := s.Repo.countActiveDevices(ctx, s.Repo.Pool, userID)
	if err != nil {
		return Me{}, err
	}
	bal, err := s.Repo.balanceMicro(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	pending, err := s.Repo.pendingOrder(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	notices, err := s.Repo.securityNotices(ctx, userID, deviceID, now.Add(-noticeWindow))
	if err != nil {
		return Me{}, err
	}
	s.Repo.touchDevice(ctx, deviceID, now)

	ent := Entitlement{
		Tier: e.Tier, PlanName: e.PlanName, ExpireAt: e.ExpireAt, PeriodQuotaBytes: e.PeriodQuotaBytes,
		PeriodUsedBytes: e.PeriodUsedBytes, PeriodResetAt: e.PeriodResetAt, TrafficPacks: packs,
		DeviceLimit: limit, DeviceCount: count,
	}
	ent.CanConnect, ent.BlockedReason = connectability(u.Status, e, packs, now)
	loginAddress, err := s.loginAddress(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	return Me{
		AccountNo: u.AccountNo, LoginAddress: loginAddress, Status: u.Status, BackupAcknowledged: u.BackupAckAt != nil,
		Entitlement: ent, Balance: money.Micro(bal).String(), PendingOrder: pending,
		SecurityNotices: notices, AnnouncementsUnread: 0, ServerTime: now,
	}, nil
}

// connectability 决定首页连接按钮的状态（方案 5.2、11.4）：
// 时长套餐有效且（无周期配额或未用完）→ 可连；否则有剩余流量包 → 可连；否则按原因阻断。
func connectability(status string, e entitlementRow, packs []TrafficPack, now time.Time) (bool, *string) {
	reason := func(r string) (bool, *string) { return false, &r }
	if status != "ACTIVE" {
		return reason("SUSPENDED")
	}
	timeValid := e.Tier > 0 && e.ExpireAt != nil && e.ExpireAt.After(now)
	if timeValid {
		if e.PeriodQuotaBytes == nil || e.PeriodUsedBytes < *e.PeriodQuotaBytes {
			return true, nil
		}
		if len(packs) > 0 {
			return true, nil
		}
		return reason("QUOTA_EXHAUSTED")
	}
	if len(packs) > 0 {
		return true, nil
	}
	if e.Tier > 0 || e.ExpireAt != nil {
		return reason("EXPIRED")
	}
	return reason("NO_PLAN")
}

// ---------- helpers ----------

func validateDevice(d DeviceInfo) error {
	if _, err := uuid.Parse(d.DeviceUID); err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "device_uid 必须是 UUID")
	}
	switch d.Platform {
	case "ANDROID", "IOS", "WINDOWS", "MACOS":
	default:
		return httpx.NewError(httpx.ErrBadRequest, "platform 无效")
	}
	if _, err := auth.ParsePublicKey(d.DevicePubKey); err != nil {
		return httpx.NewError(httpx.ErrBadRequest, "设备公钥格式错误")
	}
	if len(d.Model) > 64 || len(d.OSVersion) > 32 || len(d.AppVersion) > 32 || len(d.FingerprintHash) > 64 {
		return httpx.NewError(httpx.ErrBadRequest, "设备信息过长")
	}
	return nil
}

func (s *Service) newDevice(ctx context.Context, tx pgx.Tx, userID int64, d DeviceInfo) (int64, error) {
	name := d.Model
	if name == "" {
		name = d.Platform
	}
	deviceID, err := s.Repo.insertDevice(ctx, tx, userID, d, name)
	if err != nil {
		return 0, err
	}
	// 代理凭证按设备发放（方案 8.4）：VLESS UUID + Trojan/SS 口令（加密存储）
	secret := make([]byte, 24)
	if _, err := readRand(secret); err != nil {
		return 0, err
	}
	enc, err := s.Enc.Encrypt([]byte(hex.EncodeToString(secret)), fieldenc.AAD("device_credentials", "proxy_secret_enc", deviceID))
	if err != nil {
		return 0, err
	}
	if err := s.Repo.insertCredential(ctx, tx, deviceID, uuid.NewString(), enc); err != nil {
		return 0, err
	}
	return deviceID, nil
}

func bindMethod(m string) string {
	switch m {
	case "APK_CHANNEL", "CLIPBOARD", "MANUAL":
		return m
	}
	return "MANUAL"
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

var errShortRead = fmt.Errorf("short random read")
