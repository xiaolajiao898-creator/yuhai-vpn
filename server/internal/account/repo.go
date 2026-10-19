package account

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/auth"
)

// Repo 是本模块用到的全部 SQL；显式列出便于评审。资金类 SQL 走 sqlc（internal/db/queries）。
type Repo struct{ Pool *pgxpool.Pool }

type userRow struct {
	ID            int64
	AccountNo     string
	InviteCode    string
	Status        string
	SecretVersion int
	BackupAckAt   *time.Time
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ---- users ----

func (r *Repo) insertUser(ctx context.Context, tx pgx.Tx, accountNo, secretLookup, inviteCode, channel, ipHash, ipPrefixHash string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO users (account_no, secret_lookup, invite_code, register_channel, register_ip_hash, register_ip_prefix_hash)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''))
		RETURNING id`, accountNo, secretLookup, inviteCode, channel, ipHash, ipPrefixHash).Scan(&id)
	return id, err
}

func (r *Repo) userBySecretLookup(ctx context.Context, lookup string) (userRow, error) {
	var u userRow
	err := r.Pool.QueryRow(ctx, `
		SELECT id, account_no, invite_code, status, secret_version, backup_acknowledged_at
		  FROM users WHERE secret_lookup = $1`, lookup).
		Scan(&u.ID, &u.AccountNo, &u.InviteCode, &u.Status, &u.SecretVersion, &u.BackupAckAt)
	return u, err
}

func (r *Repo) userByID(ctx context.Context, id int64) (userRow, error) {
	var u userRow
	err := r.Pool.QueryRow(ctx, `
		SELECT id, account_no, invite_code, status, secret_version, backup_acknowledged_at
		  FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.AccountNo, &u.InviteCode, &u.Status, &u.SecretVersion, &u.BackupAckAt)
	return u, err
}

func (r *Repo) inviterByCode(ctx context.Context, tx pgx.Tx, code string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE invite_code = $1 AND status = 'ACTIVE'`, code).Scan(&id)
	return id, err
}

func (r *Repo) ackBackup(ctx context.Context, userID int64, now time.Time) error {
	_, err := r.Pool.Exec(ctx, `UPDATE users SET backup_acknowledged_at = COALESCE(backup_acknowledged_at, $2), updated_at = now() WHERE id = $1`, userID, now)
	return err
}

// ---- devices ----

type deviceIdentity struct {
	ID     int64
	UserID int64
	Status string
}

func (r *Repo) findDeviceByUID(ctx context.Context, q pgxQuerier, uid string) (deviceIdentity, error) {
	var d deviceIdentity
	err := q.QueryRow(ctx, `SELECT id, user_id, status FROM devices WHERE device_uid = $1`, uid).Scan(&d.ID, &d.UserID, &d.Status)
	return d, err
}

// resumeDeviceRecord 把同一 device_uid 重新登录到原设备行：更新公钥与机型，必要时从 REVOKED 恢复，并作废该设备上的旧刷新令牌。
func (r *Repo) resumeDeviceRecord(ctx context.Context, tx pgx.Tx, id int64, d DeviceInfo, displayName string, now time.Time) error {
	// Same fence as device revocation: a refresh already inside its transaction
	// must finish before we revoke, or its new descendant would survive.
	if err := r.lockDeviceSessions(ctx, tx, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE devices
		   SET pubkey = $2, platform = $3, model = NULLIF($4, ''), os_version = NULLIF($5, ''),
		       app_version = NULLIF($6, ''), fingerprint_hash = NULLIF($7, ''), display_name = $8,
		       status = 'ACTIVE', revoked_at = NULL, last_seen_at = $9
		 WHERE id = $1`, id, d.DevicePubKey, d.Platform, d.Model, d.OSVersion, d.AppVersion, d.FingerprintHash, displayName, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("device row missing during resume")
	}
	if _, err := tx.Exec(ctx, `UPDATE device_credentials SET status = 'ACTIVE' WHERE device_id = $1 AND status = 'REVOKED'`, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE device_id = $1 AND revoked_at IS NULL`, id, now)
	return err
}

func (r *Repo) insertDevice(ctx context.Context, tx pgx.Tx, userID int64, d DeviceInfo, displayName string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO devices (user_id, device_uid, pubkey, platform, model, os_version, app_version, fingerprint_hash, display_name)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), $9)
		RETURNING id`, userID, d.DeviceUID, d.DevicePubKey, d.Platform, d.Model, d.OSVersion, d.AppVersion, d.FingerprintHash, displayName).Scan(&id)
	return id, err
}

func (r *Repo) insertCredential(ctx context.Context, tx pgx.Tx, deviceID int64, proxyUUID string, secretEnc []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO device_credentials (device_id, proxy_uuid, proxy_secret_enc) VALUES ($1, $2, $3)`, deviceID, proxyUUID, secretEnc)
	return err
}

func (r *Repo) countActiveDevices(ctx context.Context, q pgxQuerier, userID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM devices WHERE user_id = $1 AND status = 'ACTIVE'`, userID).Scan(&n)
	return n, err
}

func (r *Repo) listDevices(ctx context.Context, userID int64) ([]DeviceSummary, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, COALESCE(display_name, ''), platform, COALESCE(model, ''), last_seen_at, created_at
		  FROM devices WHERE user_id = $1 AND status = 'ACTIVE' ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceSummary
	for rows.Next() {
		var d DeviceSummary
		var id int64
		if err := rows.Scan(&id, &d.DisplayName, &d.Platform, &d.Model, &d.LastSeenAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.ID = strconv.FormatInt(id, 10)
		out = append(out, d)
	}
	return out, rows.Err()
}

// revokeDevice 下线设备、吊销凭证、作废会话；返回是否从 ACTIVE 改为 REVOKED。
func (r *Repo) revokeDevice(ctx context.Context, tx pgx.Tx, userID, deviceID int64, now time.Time) (bool, error) {
	return RevokeDeviceInTx(ctx, tx, userID, deviceID, now)
}

// RevokeDeviceInTx is the common revocation primitive. Callers must hold the
// owning users row lock before calling, and append their actor-specific audit
// and node events in this same transaction. It never commits or deletes history.
// Acquire this refresh fence before any device/session row locks.
func RevokeDeviceInTx(ctx context.Context, tx pgx.Tx, userID, deviceID int64, now time.Time) (bool, error) {
	if tx == nil || userID <= 0 || deviceID <= 0 || now.IsZero() {
		return false, errors.New("device revocation requires a transaction, valid IDs and time")
	}
	// 所有撤销入口（退出、设备替换、设备管理）都必须先等待正在进行的
	// 刷新提交。只锁设备行不能阻止刷新插入外键 KEY SHARE 兼容的新会话，
	// 而下面 UPDATE 的快照会漏掉等待期间才提交的后代令牌。
	if err := (&Repo{}).lockDeviceSessions(ctx, tx, deviceID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE devices SET status = 'REVOKED', revoked_at = $3 WHERE id = $1 AND user_id = $2 AND status = 'ACTIVE'`, deviceID, userID, now)
	if err != nil {
		return false, err
	}
	changed := tag.RowsAffected() != 0
	if !changed {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE id=$1 AND user_id=$2 AND status='REVOKED')`, deviceID, userID).Scan(&exists); err != nil || !exists {
			return false, err
		}
	}
	// Also repair legacy already-revoked devices with surviving credentials or
	// refresh descendants. Preserve their original (possibly unknown) time.
	if _, err := tx.Exec(ctx, `UPDATE device_credentials SET status = 'REVOKED' WHERE device_id = $1 AND status='ACTIVE'`, deviceID); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE device_id = $1 AND revoked_at IS NULL`, deviceID, now)
	return changed, err
}

func (r *Repo) touchDevice(ctx context.Context, deviceID int64, now time.Time) {
	_, _ = r.Pool.Exec(ctx, `UPDATE devices SET last_seen_at = $2 WHERE id = $1`, deviceID, now)
}

func (r *Repo) insertBindLog(ctx context.Context, tx pgx.Tx, userID, deviceID int64, action, operatorType string, ipHash, idemKey string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO device_bind_logs (user_id, device_id, action, operator_type, ip_hash, idempotency_key)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), $6) ON CONFLICT (idempotency_key) DO NOTHING`,
		userID, deviceID, action, operatorType, ipHash, idemKey)
	return err
}

// LookupDevice 供鉴权中间件使用。
func (r *Repo) LookupDevice(ctx context.Context, deviceID int64) (auth.DevicePrincipal, error) {
	return r.lookupDevice(ctx, r.Pool, deviceID)
}

// 事务内使用现有连接，避免连接池已满时再次借用连接而死锁。
func (r *Repo) lookupDevice(ctx context.Context, q pgxQuerier, deviceID int64) (auth.DevicePrincipal, error) {
	var p auth.DevicePrincipal
	var status string
	err := q.QueryRow(ctx, `
		SELECT d.id, d.user_id, d.device_uid::text, d.pubkey, d.status, u.secret_version, u.status
		  FROM devices d JOIN users u ON u.id = d.user_id WHERE d.id = $1`, deviceID).
		Scan(&p.DeviceID, &p.UserID, &p.DeviceUID, &p.PubKeySPKI, &status, &p.SecretVersion, &p.UserStatus)
	p.DeviceActive = status == "ACTIVE"
	return p, err
}

// ---- entitlement bootstrap ----

func (r *Repo) initEntitlement(ctx context.Context, tx pgx.Tx, userID int64) error {
	if _, err := tx.Exec(ctx, `INSERT INTO subscriptions (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO user_balances (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
	return err
}

func (r *Repo) bindInvite(ctx context.Context, tx pgx.Tx, inviteeID, inviterID int64, code, method string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO invite_relations (invitee_user_id, inviter_user_id, invite_code, bind_method)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, inviteeID, inviterID, code, method)
	return err
}

// deviceLimit（方案 5.2）：max(时长套餐设备数, 有效流量包设备数, 2) + 未过期加购设备数之和。
// 没有任何套餐时为 2，便于多台设备查看账户与充值，但不能连接。
func (r *Repo) deviceLimit(ctx context.Context, q pgxQuerier, userID int64, now time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT GREATEST(2,
		  COALESCE((SELECT device_limit FROM subscriptions WHERE user_id = $1 AND tier > 0 AND expire_at > $2), 0),
		  COALESCE((SELECT max(device_limit) FROM traffic_packs WHERE user_id = $1 AND status = 'ACTIVE' AND expire_at > $2 AND used_bytes < total_bytes), 0))
		  + COALESCE((SELECT sum(quantity) FROM device_addons WHERE user_id = $1 AND expire_at > $2), 0)`, userID, now).Scan(&n)
	return n, err
}

// ---- sessions ----

// lockDeviceSessions 必须先于该设备的会话/撤销行锁；已持有同一事务锁时可重入。
func (r *Repo) lockDeviceSessions(ctx context.Context, tx pgx.Tx, deviceID int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "refresh-device:"+strconv.FormatInt(deviceID, 10))
	return err
}

type sessionRow struct {
	ID            int64
	UserID        int64
	DeviceID      int64
	FamilyID      string
	SecretVersion int
	ExpiresAt     time.Time
	RotatedAt     *time.Time
	RevokedAt     *time.Time
}

func (r *Repo) insertSession(ctx context.Context, q pgxQuerier, userID, deviceID int64, refreshHash, familyID string, secretVersion int, expiresAt time.Time) error {
	_, err := q.Exec(ctx, `
		INSERT INTO auth_sessions (user_id, device_id, refresh_hash, family_id, secret_version, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, deviceID, refreshHash, familyID, secretVersion, expiresAt)
	return err
}

func (r *Repo) sessionByHashForUpdate(ctx context.Context, tx pgx.Tx, hash string) (sessionRow, error) {
	var s sessionRow
	err := tx.QueryRow(ctx, `
		SELECT id, user_id, device_id, family_id::text, secret_version, expires_at, rotated_at, revoked_at
		  FROM auth_sessions WHERE refresh_hash = $1 FOR UPDATE`, hash).
		Scan(&s.ID, &s.UserID, &s.DeviceID, &s.FamilyID, &s.SecretVersion, &s.ExpiresAt, &s.RotatedAt, &s.RevokedAt)
	return s, err
}

func (r *Repo) markRotated(ctx context.Context, tx pgx.Tx, id int64, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE auth_sessions SET rotated_at = $2 WHERE id = $1`, id, now)
	return err
}

func (r *Repo) revokeFamily(ctx context.Context, q pgxQuerier, familyID string, now time.Time) error {
	_, err := q.Exec(ctx, `UPDATE auth_sessions SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, now)
	return err
}

// ---- me ----

type entitlementRow struct {
	Tier             int
	PlanName         string
	ExpireAt         *time.Time
	DeviceLimit      int
	PeriodQuotaBytes *int64
	PeriodUsedBytes  int64
	PeriodResetAt    *time.Time
}

func (r *Repo) entitlement(ctx context.Context, userID int64) (entitlementRow, error) {
	var e entitlementRow
	err := r.Pool.QueryRow(ctx, `
		SELECT s.tier, COALESCE(p.name, ''), s.expire_at, s.device_limit, s.period_quota_bytes, s.period_used_bytes, s.period_reset_at
		  FROM subscriptions s LEFT JOIN plans p ON p.id = s.plan_id WHERE s.user_id = $1`, userID).
		Scan(&e.Tier, &e.PlanName, &e.ExpireAt, &e.DeviceLimit, &e.PeriodQuotaBytes, &e.PeriodUsedBytes, &e.PeriodResetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil
	}
	return e, err
}

func (r *Repo) balanceMicro(ctx context.Context, userID int64) (int64, error) {
	var m int64
	err := r.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT available_micro FROM user_balances WHERE user_id = $1), 0)`, userID).Scan(&m)
	return m, err
}

func (r *Repo) activePacks(ctx context.Context, userID int64, now time.Time) ([]TrafficPack, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, total_bytes - used_bytes, expire_at FROM traffic_packs
		 WHERE user_id = $1 AND status = 'ACTIVE' AND expire_at > $2 ORDER BY expire_at`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrafficPack{}
	for rows.Next() {
		var p TrafficPack
		var id int64
		if err := rows.Scan(&id, &p.RemainingBytes, &p.ExpireAt); err != nil {
			return nil, err
		}
		p.ID = strconv.FormatInt(id, 10)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repo) pendingOrder(ctx context.Context, userID int64) (*PendingOrder, error) {
	var o PendingOrder
	err := r.Pool.QueryRow(ctx, `
		SELECT order_no, status, expires_at FROM orders
		 WHERE user_id = $1 AND status IN ('PENDING', 'CONFIRMING') ORDER BY created_at DESC LIMIT 1`, userID).
		Scan(&o.OrderNo, &o.Status, &o.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &o, err
}

// 近 7 天其他设备的登录记录，作为“有新设备登录”提醒（方案 4.3）。
func (r *Repo) securityNotices(ctx context.Context, userID, currentDevice int64, since time.Time) ([]SecurityNotice, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT l.id, l.created_at, COALESCE(d.display_name, d.platform)
		  FROM device_bind_logs l JOIN devices d ON d.id = l.device_id
		 WHERE l.user_id = $1 AND l.device_id <> $2 AND l.action = 'RESTORE' AND l.created_at > $3
		 ORDER BY l.created_at DESC LIMIT 5`, userID, currentDevice, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SecurityNotice{}
	for rows.Next() {
		var n SecurityNotice
		var id int64
		if err := rows.Scan(&id, &n.CreatedAt, &n.DeviceName); err != nil {
			return nil, err
		}
		n.ID, n.Type = strconv.FormatInt(id, 10), "NEW_DEVICE"
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- idempotency (api_idempotency) ----

type idemRow struct {
	RequestHash string
	StatusCode  int
	Response    []byte
	CreatedAt   time.Time
}

func (r *Repo) getIdem(ctx context.Context, q pgxQuerier, userID int64, key, endpoint string) (*idemRow, error) {
	var row idemRow
	err := q.QueryRow(ctx, `SELECT request_hash, COALESCE(status_code, 0), COALESCE(response, '{}'), created_at
		FROM api_idempotency WHERE user_id = $1 AND idem_key = $2 AND endpoint = $3`, userID, key, endpoint).
		Scan(&row.RequestHash, &row.StatusCode, &row.Response, &row.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &row, err
}

func (r *Repo) putIdem(ctx context.Context, q pgxQuerier, userID int64, key, endpoint, requestHash string, status int, response []byte) error {
	_, err := q.Exec(ctx, `
		INSERT INTO api_idempotency (user_id, idem_key, endpoint, request_hash, status_code, response)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id, idem_key) DO NOTHING`, userID, key, endpoint, requestHash, status, response)
	return err
}

func (r *Repo) clearIdemResponse(ctx context.Context, userID int64, key string) error {
	_, err := r.Pool.Exec(ctx, `UPDATE api_idempotency SET response = NULL WHERE user_id = $1 AND idem_key = $2`, userID, key)
	return err
}

// pgxQuerier 同时接受 *pgxpool.Pool 与 pgx.Tx。
type pgxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
