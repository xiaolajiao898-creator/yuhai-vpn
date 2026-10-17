package admin

import (
	"context"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// ManagedDevice is a public record projection, not an online-session or
// connection entitlement. It deliberately excludes device_uid and credentials.
type ManagedDevice struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	DisplayName *string    `json:"display_name"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
}

type ManagedDevicePage struct {
	UserID   string          `json:"user_id"`
	Items    []ManagedDevice `json:"items"`
	Total    string          `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

type ManagedDeviceFilter struct {
	Page, PageSize int
	Status         string
}

func normalizeManagedDeviceFilter(f ManagedDeviceFilter) (ManagedDeviceFilter, error) {
	var err error
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return f, err
	}
	if f.Status != "" && f.Status != "ACTIVE" && f.Status != "REVOKED" {
		return f, httpx.NewError(httpx.ErrBadRequest, "设备状态筛选无效")
	}
	return f, nil
}

func validateManagedDevice(d ManagedDevice) error {
	if _, err := positiveApprovalID(d.ID); err != nil {
		return httpx.Internal
	}
	if d.Platform != "ANDROID" && d.Platform != "IOS" || d.Status != "ACTIVE" && d.Status != "REVOKED" {
		return httpx.Internal
	}
	// A historical revocation may have no recorded time. Do not invent one.
	// A late best-effort touch may also be recorded after revocation.
	if d.Status == "ACTIVE" && d.RevokedAt != nil {
		return httpx.Internal
	}
	if d.DisplayName != nil {
		if !utf8.ValidString(*d.DisplayName) || utf8.RuneCountInString(*d.DisplayName) > 64 {
			return httpx.Internal
		}
		for _, r := range *d.DisplayName {
			if unicode.IsControl(r) {
				return httpx.Internal
			}
		}
	}
	return nil
}

func (s *UserReadService) Devices(ctx context.Context, p Principal, userID int64, f ManagedDeviceFilter) (out ManagedDevicePage, err error) {
	if userID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
	}
	f, err = normalizeManagedDeviceFilter(f)
	if err != nil {
		return out, err
	}
	// Reuse fresh session/permission checks on both sides of the bounded
	// read-only repeatable-read transaction. users.read alone is sufficient.
	err = s.read(ctx, p, false, func(c context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NewError(httpx.ErrNotFound, "用户不存在")
		}
		const where = ` WHERE d.user_id=$1 AND ($2='' OR d.status=$2)`
		var total int64
		if err := tx.QueryRow(c, `SELECT count(*) FROM devices d`+where, userID, f.Status).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(c, `SELECT d.id::text,d.platform,d.display_name,d.status,d.created_at,d.last_seen_at,d.revoked_at FROM devices d`+where+` ORDER BY d.created_at DESC,d.id DESC LIMIT $3 OFFSET $4`, userID, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = ManagedDevicePage{UserID: strconv.FormatInt(userID, 10), Items: []ManagedDevice{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize}
		for rows.Next() {
			var d ManagedDevice
			if err := rows.Scan(&d.ID, &d.Platform, &d.DisplayName, &d.Status, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt); err != nil {
				return err
			}
			if err := validateManagedDevice(d); err != nil {
				return err
			}
			out.Items = append(out.Items, d)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedDevicePage{}, err
	}
	return out, nil
}
