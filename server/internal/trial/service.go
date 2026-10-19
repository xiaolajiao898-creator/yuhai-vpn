package trial

import (
	"context"
	"errors"
	"time"

	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type Service struct {
	Pool  *pgxpool.Pool
	Clock clock.Clock
}

// Status 是首页在用户点「免费试用」之前看到的状态，不会发放权益。
type Status struct {
	Enabled          bool       `json:"enabled"`
	Eligible         bool       `json:"eligible"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason,omitempty"`
	ExpireAt         *time.Time `json:"expire_at,omitempty"`
	Tier             int        `json:"tier"`
	DeviceLimit      int        `json:"device_limit"`
	UnlimitedTraffic bool       `json:"unlimited_traffic"`
	DurationHours    int        `json:"duration_hours"`
}

// Grant 是领取结果。Status 为 granted 或 already；already 不会把到期时间再延长。
type Grant struct {
	Status           string    `json:"status"`
	ExpireAt         time.Time `json:"expire_at"`
	Tier             int       `json:"tier"`
	DeviceLimit      int       `json:"device_limit"`
	UnlimitedTraffic bool      `json:"unlimited_traffic"`
	DurationHours    int       `json:"duration_hours"`
	Active           bool      `json:"active"`
}

func (s *Service) ready() error {
	if s == nil || s.Pool == nil || s.Clock == nil {
		return httpx.Internal
	}
	return nil
}

func (s *Service) enabled(ctx context.Context, tx pgx.Tx) (bool, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT value FROM system_configs WHERE key = $1`, configKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Enabled(nil, true), nil
	}
	if err != nil {
		return false, err
	}
	return Enabled(raw, false), nil
}

// View 只读。不会插入试用记录。
func (s *Service) View(ctx context.Context, userID, deviceID int64) (Status, error) {
	out := Status{Tier: Tier, DeviceLimit: DeviceLimit, UnlimitedTraffic: true, DurationHours: DurationHours, Status: "available", Eligible: true}
	if err := s.ready(); err != nil || userID <= 0 || deviceID <= 0 {
		return out, httpx.Internal
	}
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		on, err := s.enabled(ctx, tx)
		if err != nil {
			return err
		}
		out.Enabled = on
		if !on {
			out.Eligible = false
			out.Status = "disabled"
			out.Reason = "DISABLED"
			return nil
		}
		grant, err := loadGrant(ctx, tx, userID)
		if err != nil {
			return err
		}
		if grant != nil {
			out.ExpireAt = &grant.expire
			if grant.expire.After(s.Clock.Now()) {
				out.Status = "active"
			} else {
				out.Status = "expired"
			}
			out.Eligible = false
			out.Reason = "ALREADY"
			return nil
		}
		if used, err := deviceUsedByOther(ctx, tx, userID, deviceID); err != nil {
			return err
		} else if used {
			out.Eligible = false
			out.Status = "ineligible"
			out.Reason = "DEVICE_USED"
			return nil
		}
		if prior, err := accountNotNew(ctx, tx, userID); err != nil {
			return err
		} else if prior {
			out.Eligible = false
			out.Status = "ineligible"
			out.Reason = "NOT_NEW"
		}
		return nil
	})
	return out, err
}

// Claim 发放试用。同一账户重复调用返回第一次的到期时间。
func (s *Service) Claim(ctx context.Context, userID, deviceID int64, installRaw string) (Grant, error) {
	var none Grant
	install, err := ParseInstallID(installRaw)
	if err != nil {
		return none, ErrBadInstall
	}
	if err := s.ready(); err != nil || userID <= 0 || deviceID <= 0 {
		return none, httpx.Internal
	}
	var out Grant
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "trial-user:"+strconv.FormatInt(userID, 10)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "trial-install:"+install.String()); err != nil {
			return err
		}
		on, err := s.enabled(ctx, tx)
		if err != nil {
			return err
		}
		if !on {
			return ErrDisabled
		}
		now := s.Clock.Now()
		existing, err := loadGrant(ctx, tx, userID)
		if err != nil {
			return err
		}
		if existing != nil {
			out = grantFrom(existing.expire, now, "already")
			return nil
		}
		fp, err := loadDevice(ctx, tx, userID, deviceID)
		if err != nil {
			return err
		}
		if used, err := installUsedByOther(ctx, tx, userID, install); err != nil {
			return err
		} else if used {
			return ErrDeviceUsed
		}
		if used, err := deviceUsedByOther(ctx, tx, userID, deviceID); err != nil {
			return err
		} else if used {
			return ErrDeviceUsed
		}
		if fp != "" {
			if used, err := fingerprintUsedByOther(ctx, tx, userID, fp); err != nil {
				return err
			} else if used {
				return ErrDeviceUsed
			}
		}
		if prior, err := accountNotNew(ctx, tx, userID); err != nil {
			return err
		} else if prior {
			return ErrNotNew
		}
		var storedFP *string
		if fp != "" {
			storedFP = &fp
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO trial_grants (user_id, device_id, install_id, fingerprint_hash, granted_at, expire_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			userID, deviceID, install, storedFP, now, now.Add(entitlement.TrialDuration)); err != nil {
			if isUnique(err) {
				return ErrDeviceUsed
			}
			return err
		}
		applied, err := entitlement.ApplyTrial(ctx, tx, entitlement.TrialGrant{UserID: userID}, now)
		if err != nil {
			return err
		}
		status := "granted"
		if applied.AlreadyApplied {
			status = "already"
		}
		out = grantFrom(applied.ExpireAt, now, status)
		return nil
	})
	return out, err
}

type storedGrant struct {
	expire time.Time
}

func loadGrant(ctx context.Context, tx pgx.Tx, userID int64) (*storedGrant, error) {
	var expire time.Time
	err := tx.QueryRow(ctx, `SELECT expire_at FROM trial_grants WHERE user_id = $1`, userID).Scan(&expire)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &storedGrant{expire: expire}, nil
}

func loadDevice(ctx context.Context, tx pgx.Tx, userID, deviceID int64) (string, error) {
	var owner int64
	var status string
	var fp *string
	err := tx.QueryRow(ctx, `SELECT user_id, status, fingerprint_hash FROM devices WHERE id = $1`, deviceID).Scan(&owner, &status, &fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.NewError(httpx.ErrBadRequest, "当前设备不可用")
	}
	if err != nil {
		return "", err
	}
	if owner != userID || status != "ACTIVE" {
		return "", httpx.NewError(httpx.ErrBadRequest, "当前设备不可用")
	}
	if fp == nil {
		return "", nil
	}
	return *fp, nil
}

func accountNotNew(ctx context.Context, tx pgx.Tx, userID int64) (bool, error) {
	var ledger bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM subscription_ledger WHERE user_id = $1)`, userID).Scan(&ledger); err != nil {
		return false, err
	}
	if ledger {
		return true, nil
	}
	var tier int
	var expire *time.Time
	err := tx.QueryRow(ctx, `SELECT tier, expire_at FROM subscriptions WHERE user_id = $1`, userID).Scan(&tier, &expire)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if tier > 0 || expire != nil {
		return true, nil
	}
	return false, nil
}

func deviceUsedByOther(ctx context.Context, tx pgx.Tx, userID, deviceID int64) (bool, error) {
	var other int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM trial_grants WHERE device_id = $1`, deviceID).Scan(&other)
	if errors.Is(err, pgx.ErrNoRows) {
		var fp *string
		if err = tx.QueryRow(ctx, `SELECT fingerprint_hash FROM devices WHERE id = $1 AND user_id = $2`, deviceID, userID).Scan(&fp); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		if fp == nil || *fp == "" {
			return false, nil
		}
		return fingerprintUsedByOther(ctx, tx, userID, *fp)
	}
	if err != nil {
		return false, err
	}
	return other != userID, nil
}

func installUsedByOther(ctx context.Context, tx pgx.Tx, userID int64, install uuid.UUID) (bool, error) {
	var other int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM trial_grants WHERE install_id = $1`, install).Scan(&other)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return other != userID, nil
}

func fingerprintUsedByOther(ctx context.Context, tx pgx.Tx, userID int64, fp string) (bool, error) {
	var other int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM trial_grants WHERE fingerprint_hash = $1`, fp).Scan(&other)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return other != userID, nil
}

func grantFrom(expire, now time.Time, status string) Grant {
	return Grant{
		Status: status, ExpireAt: expire, Tier: Tier, DeviceLimit: DeviceLimit,
		UnlimitedTraffic: true, DurationHours: DurationHours, Active: expire.After(now),
	}
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
