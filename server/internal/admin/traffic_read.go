package admin

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 流量统计只读（方案 8.5 / admin.yaml GET /traffic/stats）。
//
// 第 18 轮把「节点采样 → 真实 mTLS → 入库 → 扣权益」这条链路打通了，但入库之后
// **运维没有任何接口能看到这些数据**——traffic_daily 里堆着行，只能连库查。这个接口补上这一段。
//
// 三条必须说清楚的边界，写在这里也写进契约描述，免得看板上的数字被当成别的东西：
//
//  1. **这是账面用量，不是链路真实转发量。** traffic_daily 只装已被控制面接受并入库的上报：
//     节点没报、报了被拒、凭据已无法归属到设备的那部分，这里一个字节都看不到。
//     数字偏小是常态，不能反过来当成「用户只用了这么多」的证据。
//  2. **不是实时值。** 明细按上报窗口结束时刻的 UTC 日期累加，节点上报有间隔，
//     当天的数字随时会往上长。
//  3. **不越权带出标识信息。** 只给编号，不 join nodes 取名称、不 join users 取账号——
//     那些属于 nodes.read 与 users.read，拿 traffic.read 不该顺带看到。
//
// 字节数一律字符串：单日单设备就可能超过 2^53，JSON 数字会被前端读成浮点，
// 与套餐/权益侧的口径保持一致。
type TrafficReadService struct{ Auth *Service }

const trafficRead = "traffic.read"

// 统计窗口上限。366 天够覆盖一个闰年，再长的区间应该走导出而不是在线聚合——
// traffic_daily 目前只有一个 DEFAULT 分区，全表扫描的代价随时间线性增长。
const trafficMaxSpanDays = 366

// 默认窗口：最近 30 天（含今天）。
const trafficDefaultSpanDays = 30

var trafficDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

var trafficDims = map[string]bool{"day": true, "user": true, "device": true, "node": true}

// 合计表达式在 SELECT 与 ORDER BY 各出现一次，写成常量免得两处改岔。
const trafficSum = "sum(upload_bytes)+sum(download_bytes)"

type TrafficStatItem struct {
	StatDate      *string `json:"stat_date,omitempty"`
	UserID        *string `json:"user_id,omitempty"`
	DeviceID      *string `json:"device_id,omitempty"`
	NodeID        *string `json:"node_id,omitempty"`
	UploadBytes   string  `json:"upload_bytes"`
	DownloadBytes string  `json:"download_bytes"`
	TotalBytes    string  `json:"total_bytes"`
}

type TrafficStatsPage struct {
	Dim        string            `json:"dim"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Items      []TrafficStatItem `json:"items"`
	Total      string            `json:"total"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	ObservedAt time.Time         `json:"observed_at"`
}

type TrafficFilter struct {
	Dim            string
	From, To       string
	Page, PageSize int
}

// normalizeTrafficFilter 把窗口补全并校验。
//
// 刻意不做任何「善意纠正」：from 晚于 to 不交换、跨度超限不截断、日期不合法不忽略。
// 统计接口一旦自作主张改了区间，看板上的数字就和标题对不上，而没人会发现。
func normalizeTrafficFilter(f TrafficFilter, now time.Time) (TrafficFilter, time.Time, time.Time, error) {
	var zero time.Time
	var err error
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return f, zero, zero, err
	}
	if !trafficDims[f.Dim] {
		return f, zero, zero, httpx.NewError(httpx.ErrBadRequest, "统计维度无效")
	}
	parse := func(s string) (time.Time, error) {
		if !trafficDate.MatchString(s) {
			return zero, httpx.NewError(httpx.ErrBadRequest, "日期格式无效")
		}
		// time.DateOnly 会接受 2026-2-3 这类写法，正则先把位数钉死，
		// 再由 Parse 挡下 2026-02-31 这种日历上不存在的日子。
		t, e := time.ParseInLocation(time.DateOnly, s, time.UTC)
		if e != nil {
			return zero, httpx.NewError(httpx.ErrBadRequest, "日期无效")
		}
		return t, nil
	}
	today := now.UTC().Truncate(24 * time.Hour)
	to := today
	if f.To != "" {
		if to, err = parse(f.To); err != nil {
			return f, zero, zero, err
		}
	}
	from := to.AddDate(0, 0, -(trafficDefaultSpanDays - 1))
	if f.From != "" {
		if from, err = parse(f.From); err != nil {
			return f, zero, zero, err
		}
	}
	if from.After(to) {
		return f, zero, zero, httpx.NewError(httpx.ErrBadRequest, "起始日期晚于结束日期")
	}
	if to.Sub(from) > time.Duration(trafficMaxSpanDays-1)*24*time.Hour {
		return f, zero, zero, httpx.NewError(httpx.ErrBadRequest, "统计区间过长")
	}
	f.From, f.To = from.Format(time.DateOnly), to.Format(time.DateOnly)
	return f, from, to, nil
}

// trafficGroup 把维度映射成分组列与排序。分组列直接拼进 SQL，因此 dim 必须先过白名单
// （normalizeTrafficFilter 已经挡过一次，这里再取一次是为了让拼接的来源一眼可见是常量）。
//
// 排序按合计倒序，并用维度键（SELECT 的第 1–4 列，按序号引用）兜底。
// **兜底不能省**——没有全序的 ORDER BY，同一个区间翻到第二页时数据库可以合法地
// 再给一遍第一页的行。日期维度本身唯一，不需要兜底。
func trafficGroup(dim string) (cols, order string) {
	switch dim {
	case "day":
		return "stat_date::text,NULL::text,NULL::text,NULL::text", "1 DESC"
	case "user":
		return "NULL::text,user_id::text,NULL::text,NULL::text", trafficSum + " DESC,2"
	case "device":
		// 设备只属于一个用户，把 user_id 一起带出来省掉调用方再查一次；
		// 它是被 device_id 函数决定的，多这一列不会把一台设备拆成多行。
		return "NULL::text,user_id::text,device_id::text,NULL::text", trafficSum + " DESC,3"
	default:
		return "NULL::text,NULL::text,NULL::text,node_id::text", trafficSum + " DESC,4"
	}
}

// Stats 按维度聚合流量。
func (s *TrafficReadService) Stats(ctx context.Context, p Principal, f TrafficFilter) (out TrafficStatsPage, err error) {
	if s == nil || s.Auth == nil || s.Auth.Clock == nil {
		return out, httpx.Internal
	}
	f, from, to, err := normalizeTrafficFilter(f, s.Auth.Clock.Now())
	if err != nil {
		return out, err
	}
	groupCols, order := trafficGroup(f.Dim)
	read := &FinanceReadService{Auth: s.Auth}
	err = read.readWith(ctx, p, trafficRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` FROM traffic_daily WHERE stat_date>=$1::date AND stat_date<=$2::date`
		// 分组数量，不是流量总和——用来分页，别把它当成「一共用了多少」。
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*) FROM (SELECT `+groupCols+where+
			` GROUP BY 1,2,3,4) g`, from, to).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT `+groupCols+`,
 sum(upload_bytes)::text,sum(download_bytes)::text,
 (`+trafficSum+`)::text`+where+
			` GROUP BY 1,2,3,4 ORDER BY `+order+` LIMIT $3 OFFSET $4`,
			from, to, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = TrafficStatsPage{Dim: f.Dim, From: f.From, To: f.To, Items: []TrafficStatItem{},
			Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var v TrafficStatItem
			if e = rows.Scan(&v.StatDate, &v.UserID, &v.DeviceID, &v.NodeID,
				&v.UploadBytes, &v.DownloadBytes, &v.TotalBytes); e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return TrafficStatsPage{}, err
	}
	return out, nil
}
