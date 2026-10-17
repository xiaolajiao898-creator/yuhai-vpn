package account

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

type ledgerEntry struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Amount       *string   `json:"amount,omitempty"`
	DaysDelta    *int64    `json:"days_delta,omitempty"`
	SecondsDelta *int64    `json:"seconds_delta,omitempty"`
	BytesDelta   *int64    `json:"bytes_delta,omitempty"`
	DevicesDelta *int64    `json:"devices_delta,omitempty"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type ledgerPage struct {
	Items      []ledgerEntry `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// 游标只决定当前用户的翻页位置，不能提供或替代用户身份。
func ledgerParams(q url.Values, kind string, userID int64) (int, int64, error) {
	bad := func() (int, int64, error) { return 0, 0, httpx.NewError(httpx.ErrBadRequest, "分页参数无效") }
	limit := 20
	if values, exists := q["limit"]; exists {
		if len(values) != 1 {
			return bad()
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > 100 {
			return bad()
		}
		limit = n
	}
	var before int64
	if values, exists := q["cursor"]; exists {
		if len(values) != 1 || len(values[0]) > 128 {
			return bad()
		}
		raw, err := base64.RawURLEncoding.DecodeString(values[0])
		if err != nil {
			return bad()
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != kind || parts[2] != itoa64(userID) {
			return bad()
		}
		before, err = strconv.ParseInt(parts[3], 10, 64)
		if err != nil || before <= 0 {
			return bad()
		}
	}
	return limit, before, nil
}

func ledgerCursor(kind string, userID int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("v1:%s:%d:%s", kind, userID, id)))
}

type ledgerQuery func(context.Context, string, ...any) (pgx.Rows, error)

func (h *Handler) mountLedgers(v1 *gin.RouterGroup) {
	query := func(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
		return h.Svc.Repo.Pool.Query(ctx, sql, args...)
	}
	v1.GET("/balance/ledger", h.MW.S1(), serveLedger("balance", query))
	v1.GET("/entitlements/ledger", h.MW.S1(), serveLedger("entitlements", query))
}

func serveLedger(kind string, query ledgerQuery) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := httpx.UserID(c)
		if userID <= 0 {
			httpx.Fail(c, httpx.NewError(httpx.ErrBadRequest, "缺少账户身份"))
			return
		}
		limit, before, err := ledgerParams(c.Request.URL.Query(), kind, userID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		// 固定 SQL；查询参数中即使携带 user_id 也不使用，只认鉴权得到的身份。
		sql := `SELECT id, entry_type, amount_micro, COALESCE(remark, ''), created_at
		 FROM balance_ledger WHERE user_id=$1 AND ($2::bigint=0 OR id<$2)
		 ORDER BY id DESC LIMIT $3`
		if kind == "entitlements" {
			sql = `SELECT id, change_type, delta_seconds, delta_bytes, delta_devices, COALESCE(remark, ''), created_at
			 FROM subscription_ledger WHERE user_id=$1 AND ($2::bigint=0 OR id<$2)
			 ORDER BY id DESC LIMIT $3`
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		rows, err := query(ctx, sql, userID, before, limit+1)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		defer rows.Close()
		page := ledgerPage{Items: make([]ledgerEntry, 0, limit)}
		for rows.Next() {
			var e ledgerEntry
			var id, amount, seconds, bytes, devices int64
			if kind == "balance" {
				err = rows.Scan(&id, &e.Kind, &amount, &e.Note, &e.CreatedAt)
				a := money.Micro(amount).String()
				e.Amount = &a
			} else {
				err = rows.Scan(&id, &e.Kind, &seconds, &bytes, &devices, &e.Note, &e.CreatedAt)
				days := seconds / 86400
				e.DaysDelta, e.SecondsDelta, e.BytesDelta, e.DevicesDelta = &days, &seconds, &bytes, &devices
			}
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			e.ID = itoa64(id)
			if len(page.Items) == limit {
				next := ledgerCursor(kind, userID, page.Items[limit-1].ID)
				page.NextCursor = &next
				break
			}
			page.Items = append(page.Items, e)
		}
		if err := rows.Err(); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		httpx.OK(c, page)
	}
}
