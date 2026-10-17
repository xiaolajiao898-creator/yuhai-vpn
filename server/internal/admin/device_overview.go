package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 后台设备总览（admin.yaml GET /devices）。
//
// 与已有的 GET /users/{id}/devices 的区别只有一个：**不先选定用户**。
// 运维排查「这台设备是谁的」「最近新增了哪些设备」时，按用户查得先知道是哪个用户，
// 而那恰恰是要查的东西。
//
// 权限沿用 users.read，不新增能力：同样的字段按用户一个一个翻本来就能看全，
// 换个入口不构成新的暴露面，再立一个 devices.read 只会让授权表更难核对。
// 反过来说，**这里也绝不能多给一个字段**——投影与按用户查那份逐字段一致，
// 仍然不含 device_uid、公钥、指纹哈希与任何凭据。
type ManagedDeviceOverview struct {
	UserID string `json:"user_id"`
	ManagedDevice
}

type ManagedDeviceOverviewPage struct {
	Items      []ManagedDeviceOverview `json:"items"`
	Total      string                  `json:"total"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	ObservedAt time.Time               `json:"observed_at"`
}

type ManagedDeviceOverviewFilter struct {
	Page, PageSize int
	Status         string
	Platform       string
	UserID         string
}

// Overview 跨用户列出设备记录。
//
// 设备记录不是在线会话：last_seen_at 是尽力而为的活跃痕迹，ACTIVE 也不代表此刻连着。
func (s *UserReadService) Overview(ctx context.Context, p Principal, f ManagedDeviceOverviewFilter) (
	out ManagedDeviceOverviewPage, err error) {
	page, size, err := normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Status != "" && f.Status != "ACTIVE" && f.Status != "REVOKED" {
		return out, httpx.NewError(httpx.ErrBadRequest, "设备状态筛选无效")
	}
	if f.Platform != "" && f.Platform != "ANDROID" && f.Platform != "IOS" {
		return out, httpx.NewError(httpx.ErrBadRequest, "设备平台筛选无效")
	}
	// 用户编号是精确匹配。不存在的编号返回空列表而不是 404——
	// 这里是列表不是详情，「查无此人」与「此人没有设备」在这个入口上没有区别，
	// 硬要区分反而会把「用户是否存在」变成一个可以被枚举的信号。
	var userID int64
	if f.UserID != "" {
		if userID, err = positiveApprovalID(f.UserID); err != nil {
			return out, httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
		}
	}
	err = s.read(ctx, p, false, func(c context.Context, tx pgx.Tx) error {
		// $1 与字面量 0 比较会让 PostgreSQL 把参数推断成 int4，而用户编号远超 int4，
		// 真实数据一到就报错——与资金/邀请侧同一个坑，显式 ::bigint。
		const where = ` FROM devices d WHERE ($1::bigint=0 OR d.user_id=$1::bigint)
 AND ($2=''OR d.status=$2) AND ($3=''OR d.platform=$3)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, userID, f.Status, f.Platform).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT d.user_id::text,d.id::text,d.platform,d.display_name,d.status,
 d.created_at,d.last_seen_at,d.revoked_at`+where+
			` ORDER BY d.created_at DESC,d.id DESC LIMIT $4 OFFSET $5`,
			userID, f.Status, f.Platform, size, (page-1)*size)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedDeviceOverviewPage{Items: []ManagedDeviceOverview{},
			Total: strconv.FormatInt(total, 10), Page: page, PageSize: size, ObservedAt: s.Auth.Clock.Now()}
		for rows.Next() {
			var v ManagedDeviceOverview
			if e = rows.Scan(&v.UserID, &v.ID, &v.Platform, &v.DisplayName, &v.Status,
				&v.CreatedAt, &v.LastSeenAt, &v.RevokedAt); e != nil {
				return e
			}
			if e = validateManagedDevice(v.ManagedDevice); e != nil {
				return e
			}
			if _, e = positiveApprovalID(v.UserID); e != nil {
				return httpx.Internal
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedDeviceOverviewPage{}, err
	}
	return out, nil
}
