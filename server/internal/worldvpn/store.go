package worldvpn

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// PG 把批发账号口令做字段加密后落库。日志和接口列表都不读这列。
type PG struct {
	Pool   *pgxpool.Pool
	Cipher *fieldenc.Cipher
}

func (p *PG) EnsureSyncAfter(ctx context.Context, now time.Time) (time.Time, error) {
	if _, err := p.Pool.Exec(ctx, `INSERT INTO worldvpn_state(singleton, sync_after) VALUES (TRUE, $1) ON CONFLICT (singleton) DO NOTHING`, now); err != nil {
		return time.Time{}, err
	}
	var at time.Time
	err := p.Pool.QueryRow(ctx, `SELECT sync_after FROM worldvpn_state`).Scan(&at)
	return at, err
}

func (p *PG) CollectOrders(ctx context.Context, after time.Time) error {
	_, err := p.Pool.Exec(ctx, `
INSERT INTO worldvpn_grants (source, source_id, user_id, period_months, status, last_error)
SELECT 'ORDER', o.id, o.user_id,
  CASE
    WHEN (o.plan_snapshot->'plan'->>'plan_type') = 'TIME'
      AND (o.plan_snapshot->'plan'->>'duration_days') ~ '^[0-9]+$'
      AND (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 28 AND 31 THEN 1
    WHEN (o.plan_snapshot->'plan'->>'plan_type') = 'TIME'
      AND (o.plan_snapshot->'plan'->>'duration_days') ~ '^[0-9]+$'
      AND (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 89 AND 92 THEN 3
    WHEN (o.plan_snapshot->'plan'->>'plan_type') = 'TIME'
      AND (o.plan_snapshot->'plan'->>'duration_days') ~ '^[0-9]+$'
      AND (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 360 AND 366 THEN 12
    ELSE 0
  END,
  CASE
    WHEN (o.plan_snapshot->'plan'->>'plan_type') = 'TIME'
      AND (o.plan_snapshot->'plan'->>'duration_days') ~ '^[0-9]+$'
      AND (
        (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 28 AND 31
        OR (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 89 AND 92
        OR (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 360 AND 366
      ) THEN 'PENDING'
    ELSE 'SKIPPED'
  END,
  CASE
    WHEN (o.plan_snapshot->'plan'->>'plan_type') = 'TIME'
      AND NOT (
        (o.plan_snapshot->'plan'->>'duration_days') ~ '^[0-9]+$'
        AND (
          (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 28 AND 31
          OR (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 89 AND 92
          OR (o.plan_snapshot->'plan'->>'duration_days')::int BETWEEN 360 AND 366
        )
      ) THEN 'duration not a wholesale month'
    ELSE ''
  END
FROM orders o
WHERE o.status = 'COMPLETED' AND o.fulfillment_result = 'FULFILLED' AND o.completed_at >= $1
  AND NOT EXISTS (SELECT 1 FROM worldvpn_grants g WHERE g.source = 'ORDER' AND g.source_id = o.id)`, after)
	if err != nil {
		return err
	}
	// 现有兑换码表不改。周卡是 7 天，批发接口只卖整月，记成 SKIPPED，不扣点。
	if _, err = p.Pool.Exec(ctx, `
INSERT INTO worldvpn_grants (source, source_id, user_id, period_months, status, last_error)
SELECT 'REDEEM', c.id, c.redeemed_by,
  CASE
    WHEN c.duration_days BETWEEN 28 AND 31 THEN 1
    WHEN c.duration_days BETWEEN 89 AND 92 THEN 3
    WHEN c.duration_days BETWEEN 360 AND 366 THEN 12
    ELSE 0
  END,
  CASE
    WHEN c.duration_days BETWEEN 28 AND 31
      OR c.duration_days BETWEEN 89 AND 92
      OR c.duration_days BETWEEN 360 AND 366 THEN 'PENDING'
    ELSE 'SKIPPED'
  END,
  CASE
    WHEN NOT (
      c.duration_days BETWEEN 28 AND 31
      OR c.duration_days BETWEEN 89 AND 92
      OR c.duration_days BETWEEN 360 AND 366
    ) THEN 'duration not a wholesale month'
    ELSE ''
  END
FROM redeem_codes c
WHERE c.status = 'used' AND c.redeemed_by IS NOT NULL AND c.redeemed_at >= $1
  AND NOT EXISTS (SELECT 1 FROM worldvpn_grants g WHERE g.source = 'REDEEM' AND g.source_id = c.id)`, after); err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `
INSERT INTO worldvpn_grants (source, source_id, user_id, period_months, status, last_error)
SELECT 'TRIAL', t.user_id, t.user_id, 0, 'PENDING', ''
FROM trial_grants t
WHERE t.granted_at >= $1
  AND NOT EXISTS (SELECT 1 FROM worldvpn_grants g WHERE g.source = 'TRIAL' AND g.source_id = t.user_id)`, after)
	return err
}

func (p *PG) PendingGrants(ctx context.Context, limit int) ([]grant, error) {
	rows, err := p.Pool.Query(ctx, `SELECT id, user_id, period_months, source, upstream_username, upstream_password_enc FROM worldvpn_grants WHERE status='PENDING' ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []grant
	for rows.Next() {
		var g grant
		var user *string
		var blob []byte
		if err := rows.Scan(&g.ID, &g.UserID, &g.Months, &g.Source, &user, &blob); err != nil {
			return nil, err
		}
		if user != nil && *user != "" {
			if p.Cipher == nil {
				return nil, errors.New("worldvpn: 缺少字段加密")
			}
			plain, err := p.Cipher.Decrypt(blob, grantAAD(g.ID))
			if err != nil {
				return nil, err
			}
			g.Billed = true
			g.UpstreamUser = *user
			g.UpstreamPass = string(plain)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func grantAAD(id int64) []byte {
	return fieldenc.AAD("worldvpn_grants", "upstream_password_enc", id)
}

func (p *PG) RememberBilling(ctx context.Context, id int64, username, password string) error {
	if username == "" || password == "" {
		return errors.New("worldvpn: 批发账号不完整")
	}
	if p.Cipher == nil {
		return errors.New("worldvpn: 缺少字段加密")
	}
	blob, err := p.Cipher.Encrypt([]byte(password), grantAAD(id))
	if err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `UPDATE worldvpn_grants SET upstream_username=$2, upstream_password_enc=$3, updated_at=now() WHERE id=$1 AND status='PENDING'`, id, username, blob)
	return err
}

func (p *PG) AlignExpiry(ctx context.Context, now time.Time) error {
	// 只把本地记录缩短到当前订阅，不把批发侧没续过的到期时间往后拨。
	_, err := p.Pool.Exec(ctx, `
UPDATE worldvpn_accounts a
   SET expire_at = s.expire_at, updated_at = now()
  FROM subscriptions s
 WHERE s.user_id = a.user_id AND a.status = 'ACTIVE'
   AND s.tier > 0 AND s.expire_at IS NOT NULL AND s.expire_at < a.expire_at`)
	return err
}

func (p *PG) FinishGrant(ctx context.Context, id int64, status, lastErr string) error {
	if len(lastErr) > 200 {
		lastErr = lastErr[:200]
	}
	_, err := p.Pool.Exec(ctx, `UPDATE worldvpn_grants SET status=$2, last_error=$3, updated_at=now() WHERE id=$1`, id, status, lastErr)
	return err
}

func (p *PG) UserAccountNo(ctx context.Context, userID int64) (string, error) {
	var no, status string
	err := p.Pool.QueryRow(ctx, `SELECT account_no, status FROM users WHERE id=$1`, userID).Scan(&no, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "ACTIVE") {
		return "", httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	return no, err
}

func (p *PG) SubscriptionExpire(ctx context.Context, userID int64) (time.Time, bool, error) {
	var exp *time.Time
	err := p.Pool.QueryRow(ctx, `SELECT expire_at FROM subscriptions WHERE user_id=$1 AND tier > 0`, userID).Scan(&exp)
	if errors.Is(err, pgx.ErrNoRows) || exp == nil {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return *exp, true, nil
}

func (p *PG) Account(ctx context.Context, userID int64) (account, bool, error) {
	var a account
	var blob []byte
	err := p.Pool.QueryRow(ctx, `SELECT user_id, vpn_username, password_enc, status, expire_at, source, sandbox FROM worldvpn_accounts WHERE user_id=$1`, userID).
		Scan(&a.UserID, &a.Username, &blob, &a.Status, &a.ExpireAt, &a.Source, &a.Sandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return account{}, false, nil
	}
	if err != nil {
		return account{}, false, err
	}
	if p.Cipher == nil {
		return account{}, false, errors.New("worldvpn: 缺少字段加密")
	}
	plain, err := p.Cipher.Decrypt(blob, fieldenc.AAD("worldvpn_accounts", "password_enc", userID))
	if err != nil {
		return account{}, false, err
	}
	a.Password = string(plain)
	return a, true, nil
}

func (p *PG) SaveAccount(ctx context.Context, a account) error {
	blob, err := p.seal(a.UserID, a.Password)
	if err != nil {
		return err
	}
	_, err = p.Pool.Exec(ctx, `
INSERT INTO worldvpn_accounts (user_id, vpn_username, password_enc, status, expire_at, source, sandbox, updated_at)
VALUES ($1,$2,$3,'ACTIVE',$4,$5,$6,now())
ON CONFLICT (user_id) DO UPDATE SET
  vpn_username=EXCLUDED.vpn_username, password_enc=EXCLUDED.password_enc, status='ACTIVE',
  expire_at=EXCLUDED.expire_at, source=EXCLUDED.source, sandbox=EXCLUDED.sandbox, updated_at=now()`,
		a.UserID, a.Username, blob, a.ExpireAt, a.Source, a.Sandbox)
	return err
}

func (p *PG) seal(userID int64, password string) ([]byte, error) {
	if p.Cipher == nil || password == "" {
		return nil, errors.New("worldvpn: 缺少字段加密或口令")
	}
	return p.Cipher.Encrypt([]byte(password), fieldenc.AAD("worldvpn_accounts", "password_enc", userID))
}

func (p *PG) AccountsToDisable(ctx context.Context, now time.Time) ([]account, error) {
	rows, err := p.Pool.Query(ctx, `
SELECT a.user_id, a.vpn_username, a.expire_at, a.source, a.sandbox
FROM worldvpn_accounts a
JOIN users u ON u.id = a.user_id
WHERE a.status = 'ACTIVE' AND (
  u.status <> 'ACTIVE'
  OR a.expire_at <= $1
  OR NOT EXISTS (
    SELECT 1 FROM subscriptions s
     WHERE s.user_id = a.user_id AND s.tier > 0 AND s.expire_at > $1)
)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account
	for rows.Next() {
		var a account
		a.Status = "ACTIVE"
		if err := rows.Scan(&a.UserID, &a.Username, &a.ExpireAt, &a.Source, &a.Sandbox); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *PG) MarkDisabled(ctx context.Context, userID int64) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE worldvpn_accounts SET status='DISABLED', updated_at=now() WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE worldvpn_peers SET released_at=COALESCE(released_at, now()) WHERE user_id=$1 AND released_at IS NULL`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PG) PeersToRelease(ctx context.Context) ([]openPeer, error) {
	rows, err := p.Pool.Query(ctx, `
SELECT id, user_id, device_id, server_id, wireguard_id, sandbox
FROM worldvpn_peers
WHERE released_at IS NULL AND release_requested_at IS NOT NULL
ORDER BY id LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

func scanPeers(rows pgx.Rows) ([]openPeer, error) {
	var out []openPeer
	for rows.Next() {
		var p openPeer
		if err := rows.Scan(&p.ID, &p.UserID, &p.DeviceID, &p.ServerID, &p.WireGuardID, &p.Sandbox); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (p *PG) MarkPeerReleased(ctx context.Context, id int64) error {
	_, err := p.Pool.Exec(ctx, `UPDATE worldvpn_peers SET released_at=COALESCE(released_at, now()) WHERE id=$1`, id)
	return err
}

func (p *PG) LineAccess(ctx context.Context, userID int64, now time.Time, sandbox bool) (lineAccess, error) {
	var userStatus string
	var pending bool
	var accStatus *string
	var exp *time.Time
	var accSandbox *bool
	err := p.Pool.QueryRow(ctx, `
SELECT u.status,
       EXISTS (SELECT 1 FROM worldvpn_grants g WHERE g.user_id=u.id AND g.status='PENDING'),
       a.status, a.expire_at, a.sandbox
FROM users u
LEFT JOIN worldvpn_accounts a ON a.user_id = u.id
WHERE u.id=$1`, userID).Scan(&userStatus, &pending, &accStatus, &exp, &accSandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return lineAccess{}, httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	if err != nil {
		return lineAccess{}, err
	}
	switch {
	case userStatus != "ACTIVE":
		return lineAccess{Reason: "ACCOUNT"}, nil
	case accStatus != nil && *accStatus == "ACTIVE" && exp != nil && exp.After(now) && accSandbox != nil && *accSandbox == sandbox:
		return lineAccess{Available: true}, nil
	case pending:
		return lineAccess{Reason: "PROVISIONING"}, nil
	case accStatus != nil:
		return lineAccess{Reason: "EXPIRED"}, nil
	default:
		return lineAccess{Reason: "NO_PLAN"}, nil
	}
}

func (p *PG) DeviceOK(ctx context.Context, userID, deviceID int64) error {
	var status string
	err := p.Pool.QueryRow(ctx, `SELECT status FROM devices WHERE id=$1 AND user_id=$2`, deviceID, userID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "ACTIVE") {
		return httpx.NewError(httpx.ErrDeviceRevoked, "设备已被移除")
	}
	return err
}

func (p *PG) ActivePeers(ctx context.Context, userID, exceptDevice int64) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `
SELECT count(*) FROM worldvpn_peers
WHERE user_id=$1 AND device_id <> $2 AND released_at IS NULL AND release_requested_at IS NULL`, userID, exceptDevice).Scan(&n)
	return n, err
}

func (p *PG) DevicePeer(ctx context.Context, userID, deviceID int64) (openPeer, bool, error) {
	var pPeer openPeer
	err := p.Pool.QueryRow(ctx, `
SELECT id, user_id, device_id, server_id, wireguard_id, sandbox
FROM worldvpn_peers
WHERE user_id=$1 AND device_id=$2 AND released_at IS NULL`, userID, deviceID).
		Scan(&pPeer.ID, &pPeer.UserID, &pPeer.DeviceID, &pPeer.ServerID, &pPeer.WireGuardID, &pPeer.Sandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return openPeer{}, false, nil
	}
	return pPeer, err == nil, err
}

func (p *PG) InsertPeer(ctx context.Context, peer openPeer) error {
	if peer.Releasing {
		_, err := p.Pool.Exec(ctx, `
INSERT INTO worldvpn_peers (user_id, device_id, server_id, wireguard_id, sandbox, release_requested_at)
VALUES ($1,$2,$3,$4,$5, now())`, peer.UserID, peer.DeviceID, peer.ServerID, peer.WireGuardID, peer.Sandbox)
		return err
	}
	_, err := p.Pool.Exec(ctx, `
INSERT INTO worldvpn_peers (user_id, device_id, server_id, wireguard_id, sandbox)
VALUES ($1,$2,$3,$4,$5)`, peer.UserID, peer.DeviceID, peer.ServerID, peer.WireGuardID, peer.Sandbox)
	return err
}

func (p *PG) DeviceLimit(ctx context.Context, userID int64) (int, error) {
	var n int
	err := p.Pool.QueryRow(ctx, `SELECT device_limit FROM subscriptions WHERE user_id=$1`, userID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return MaxActivePeersPerUser, nil
	}
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return MaxActivePeersPerUser, nil
	}
	if n > 100 {
		n = 100
	}
	return n, nil
}

func (p *PG) OpenPeers(ctx context.Context, userID int64) ([]openPeer, error) {
	rows, err := p.Pool.Query(ctx, `
SELECT id, user_id, device_id, server_id, wireguard_id, sandbox, release_requested_at IS NOT NULL
FROM worldvpn_peers
WHERE user_id=$1 AND released_at IS NULL
ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []openPeer
	for rows.Next() {
		var peer openPeer
		if err := rows.Scan(&peer.ID, &peer.UserID, &peer.DeviceID, &peer.ServerID, &peer.WireGuardID, &peer.Sandbox, &peer.Releasing); err != nil {
			return nil, err
		}
		out = append(out, peer)
	}
	return out, rows.Err()
}

func (p *PG) RequestRelease(ctx context.Context, userID, deviceID int64) (openPeer, bool, error) {
	var pPeer openPeer
	err := p.Pool.QueryRow(ctx, `
UPDATE worldvpn_peers SET release_requested_at=COALESCE(release_requested_at, now())
WHERE id = (
  SELECT id FROM worldvpn_peers WHERE user_id=$1 AND device_id=$2 AND released_at IS NULL
  ORDER BY id DESC LIMIT 1
)
RETURNING id, user_id, device_id, server_id, wireguard_id, sandbox`, userID, deviceID).
		Scan(&pPeer.ID, &pPeer.UserID, &pPeer.DeviceID, &pPeer.ServerID, &pPeer.WireGuardID, &pPeer.Sandbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return openPeer{}, false, nil
	}
	return pPeer, err == nil, err
}
