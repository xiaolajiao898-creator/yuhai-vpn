// Package traffic exposes the signed-in account's aggregate traffic history.
package traffic

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const (
	defaultDays = 30
	maxDays     = 90
	// JSON numbers are also consumed by the web client. Keep every value and
	// the entire requested period's total exact in an IEEE-754 integer.
	maxSafeBytes int64 = 1<<53 - 1
)

type DeviceUsage struct {
	DeviceID string `json:"device_id"`
	Bytes    int64  `json:"bytes"`
}

type Day struct {
	Date      string        `json:"date"`
	UpBytes   int64         `json:"up_bytes"`
	DownBytes int64         `json:"down_bytes"`
	ByDevice  []DeviceUsage `json:"by_device"`
}

type DailyUsage struct {
	Days []Day `json:"days"`
}

type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Service struct {
	Pool  Queryer
	Clock clock.Clock
}

// Daily uses UTC calendar dates, inclusive of today. Device status is not a
// filter: removing a device must not erase its historical usage. Checking both
// ownership columns also excludes malformed rows that name another account's
// device, even if their traffic_daily.user_id was populated incorrectly.
func (s *Service) Daily(ctx context.Context, userID int64, days int) (DailyUsage, error) {
	if userID <= 0 {
		return DailyUsage{}, httpx.NewError(httpx.ErrTokenMissing, "缺少账户身份")
	}
	if days < 1 || days > maxDays {
		return DailyUsage{}, badDays()
	}
	now := s.Clock.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, 1-days)
	end := today.AddDate(0, 0, 1)
	result := DailyUsage{Days: make([]Day, days)}
	index := make(map[string]int, days)
	for i := range result.Days {
		date := start.AddDate(0, 0, i).Format(time.DateOnly)
		result.Days[i] = Day{Date: date, ByDevice: []DeviceUsage{}}
		index[date] = i
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.Pool.Query(ctx, `SELECT to_char(t.stat_date, 'YYYY-MM-DD'), t.device_id,
		SUM(t.upload_bytes)::text, SUM(t.download_bytes)::text,
		BOOL_OR(t.upload_bytes < 0 OR t.download_bytes < 0)
		FROM traffic_daily t
		JOIN devices d ON d.id=t.device_id AND d.user_id=t.user_id
		WHERE t.user_id=$1 AND t.stat_date >= $2::date AND t.stat_date < $3::date
		GROUP BY t.stat_date, t.device_id ORDER BY t.stat_date, t.device_id`,
		userID, start.Format(time.DateOnly), end.Format(time.DateOnly))
	if err != nil {
		return DailyUsage{}, err
	}
	defer rows.Close()
	var periodTotal int64
	for rows.Next() {
		var date, upRaw, downRaw string
		var deviceID int64
		var negative bool
		if err := rows.Scan(&date, &deviceID, &upRaw, &downRaw, &negative); err != nil {
			return DailyUsage{}, err
		}
		i, ok := index[date]
		if !ok || deviceID <= 0 || negative {
			return DailyUsage{}, errors.New("invalid daily traffic aggregate")
		}
		// PostgreSQL SUM(bigint) produces NUMERIC, so aggregation itself cannot
		// wrap at int64. Validate before conversion and every subsequent sum.
		up, err := safeBytes(upRaw)
		if err != nil {
			return DailyUsage{}, err
		}
		down, err := safeBytes(downRaw)
		if err != nil {
			return DailyUsage{}, err
		}
		deviceTotal, err := addBytes(up, down)
		if err != nil {
			return DailyUsage{}, err
		}
		day := &result.Days[i]
		if day.UpBytes, err = addBytes(day.UpBytes, up); err != nil {
			return DailyUsage{}, err
		}
		if day.DownBytes, err = addBytes(day.DownBytes, down); err != nil {
			return DailyUsage{}, err
		}
		if periodTotal, err = addBytes(periodTotal, deviceTotal); err != nil {
			return DailyUsage{}, err
		}
		day.ByDevice = append(day.ByDevice, DeviceUsage{DeviceID: strconv.FormatInt(deviceID, 10), Bytes: deviceTotal})
	}
	if err := rows.Err(); err != nil {
		return DailyUsage{}, err
	}
	return result, nil
}

func safeBytes(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || n > maxSafeBytes {
		return 0, errors.New("traffic bytes exceed safe integer range")
	}
	return n, nil
}

func addBytes(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > maxSafeBytes || b > maxSafeBytes-a {
		return 0, errors.New("traffic total exceeds safe integer range")
	}
	return a + b, nil
}
