package trial

import (
	"context"

	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/db"
	"github.com/yuhai-vpn/server/internal/entitlement"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// ViewForAccount 给网页用户中心看试用资格。浏览器不是设备，不查设备指纹。
func (s *Service) ViewForAccount(ctx context.Context, userID int64) (Status, error) {
	out := Status{Tier: Tier, DeviceLimit: DeviceLimit, UnlimitedTraffic: true, DurationHours: DurationHours, Status: "available", Eligible: true}
	if err := s.ready(); err != nil || userID <= 0 {
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

// ClaimForAccount 发放 24 小时试用。资格、幂等和权益写入与 App 的 Claim 相同，
// 但不占用设备名额：网页登录本来就不绑定设备。
func (s *Service) ClaimForAccount(ctx context.Context, userID int64) (Grant, error) {
	var none Grant
	if err := s.ready(); err != nil || userID <= 0 {
		return none, httpx.Internal
	}
	var out Grant
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "trial-user:"+strconv.FormatInt(userID, 10)); err != nil {
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
		if prior, err := accountNotNew(ctx, tx, userID); err != nil {
			return err
		} else if prior {
			return ErrNotNew
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO trial_grants (user_id, device_id, install_id, fingerprint_hash, granted_at, expire_at)
			VALUES ($1, NULL, NULL, NULL, $2, $3)`,
			userID, now, now.Add(entitlement.TrialDuration)); err != nil {
			if isUnique(err) {
				again, loadErr := loadGrant(ctx, tx, userID)
				if loadErr != nil {
					return loadErr
				}
				if again != nil {
					out = grantFrom(again.expire, now, "already")
					return nil
				}
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
