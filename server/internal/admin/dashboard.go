package admin

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/sitestats"
)

// 后台总览（admin.yaml GET /dashboard）。
//
// 总览天然是个跨域视图：用户、节点、流量、资金、邀请分属五套独立能力。
// 最省事的做法是「有 L0 就全给」，但那等于**把五个能力的数据从一个只需要一个能力的入口漏出去**——
// 只被授了 users.read 的运营不该在首页看到今天的订单数。
//
// 因此这里的规矩是：**按调用者实际持有的能力逐段计算，没有的那段整块不出现**，
// 并且响应里带一份 `sections` 明确列出这次包含了哪几段。
// 少一段和「那段是 0」必须能区分开——首页上一个孤零零的 0 会被当成「今天没单」，
// 而真相可能是「你没有资金查看权限」。
//
// 同理，没有的那段**连查询都不发**：权限不足时连表都不碰，而不是查完再筛掉。
//
// 计数一律字符串，与其余后台接口口径一致。
type DashboardUsers struct {
	ByStatus      map[string]string `json:"by_status"`
	DevicesActive string            `json:"devices_active"`
}

type DashboardNodes struct {
	ByStatus map[string]string `json:"by_status"`
}

type DashboardTraffic struct {
	WindowDays    int    `json:"window_days"`
	From          string `json:"from"`
	To            string `json:"to"`
	UploadBytes   string `json:"upload_bytes"`
	DownloadBytes string `json:"download_bytes"`
	TotalBytes    string `json:"total_bytes"`
}

type DashboardFinance struct {
	OrdersCreatedToday    string `json:"orders_created_today"`
	DepositExceptionsOpen string `json:"deposit_exceptions_open"`
}

type DashboardInvite struct {
	RewardsInReview string `json:"rewards_in_review"`
}

type DashboardSiteWindow struct {
	Today   string `json:"today"`
	History string `json:"history"`
}

type DashboardSiteDownload struct {
	Today         string `json:"today"`
	Week          string `json:"week"`
	Month         string `json:"month"`
	PreviousMonth string `json:"previous_month"`
	History       string `json:"history"`
}

type DashboardSiteDownloads struct {
	Android       DashboardSiteDownload `json:"android"`
	Windows       DashboardSiteDownload `json:"windows"`
	AndroidLegacy DashboardSiteDownload `json:"android_legacy"`
}

// DashboardSite 的「今天 / 本周 / 本月」按 Asia/Kuala_Lumpur 自然日。
// 浏览量是限流后的次数；访客是去重哈希，历史访客不是每日访客相加。
// 下载同一哈希、同一通道、同一天只计一次。旧版安卓包单独列出，不并进当前安卓包。
type DashboardSite struct {
	Timezone  string                 `json:"timezone"`
	Pageviews DashboardSiteWindow    `json:"pageviews"`
	Visitors  DashboardSiteWindow    `json:"visitors"`
	Downloads DashboardSiteDownloads `json:"downloads"`
}

type Dashboard struct {
	Sections   []string          `json:"sections"`
	Users      *DashboardUsers   `json:"users,omitempty"`
	Nodes      *DashboardNodes   `json:"nodes,omitempty"`
	Traffic    *DashboardTraffic `json:"traffic,omitempty"`
	Finance    *DashboardFinance `json:"finance,omitempty"`
	Invite     *DashboardInvite  `json:"invite,omitempty"`
	Site       *DashboardSite    `json:"site,omitempty"`
	ObservedAt time.Time         `json:"observed_at"`
}

type DashboardService struct{ Auth *Service }

// 总览的窗口固定 7 天（含今天），不做参数——首页上一个能被调参的数字，
// 截图传出去之后没人说得清它统计的是哪一段。要自定义区间的走 GET /traffic/stats。
const dashboardTrafficDays = 7

// dashboardSections 是「段名 → 所需能力」。顺序即 sections 的输出顺序。
var dashboardSections = []struct{ name, capability string }{
	{"users", userReadPermission},
	{"nodes", nodeReadPermission},
	{"traffic", trafficRead},
	{"finance", financeRead},
	{"invite", inviteRead},
	{"site", siteStatsRead},
}

const siteStatsRead = "site.stats.read"

// held 返回调用者当前实际持有的能力集合（只认 risk_level=0 的只读能力），
// 同时完成与其他只读接口一致的会话核验。
//
// 一次查询问全部，而不是每段查一次：分多次查，中途的角色变更会让不同段落基于不同的权限快照，
// 出来的首页是几个时刻的拼接。
func (s *DashboardService) held(ctx context.Context, p Principal, codes []string) (map[string]bool, error) {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return nil, errSession
	}
	rows, err := s.Auth.Pool.Query(ctx, `SELECT DISTINCT perm.code
 FROM admin_users u JOIN admin_sessions session ON session.admin_user_id=u.id
 JOIN admin_user_roles ur ON ur.admin_user_id=u.id
 JOIN admin_role_permissions rp ON rp.role_id=ur.role_id
 JOIN admin_permissions perm ON perm.code=rp.permission_code
 WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3)
 AND session.id=$2 AND session.revoked_at IS NULL AND session.expires_at>$3
 AND session.last_active_at>$3-interval '30 minutes'
 AND perm.code = ANY($4::text[]) AND perm.risk_level=0`,
		id, p.SessionID, s.Auth.Clock.Now(), codes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			return nil, err
		}
		out[code] = true
	}
	return out, rows.Err()
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// Get 汇总总览。
func (s *DashboardService) Get(ctx context.Context, p Principal) (out Dashboard, err error) {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil {
		return out, httpx.Internal
	}
	codes := make([]string, 0, len(dashboardSections))
	for _, sec := range dashboardSections {
		codes = append(codes, sec.capability)
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	before, err := s.held(c, p, codes)
	if err != nil {
		return out, err
	}
	if len(before) == 0 {
		// 一段都看不到时给 403 而不是一个空壳：返回「什么都没有」会被当成「系统里什么都没有」。
		return out, httpx.NewError(httpx.ErrForbidden, "当前会话没有任何可查看的总览数据")
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer rollbackApproval(tx)
	now := s.Auth.Clock.Now()
	out = Dashboard{Sections: []string{}, ObservedAt: now}
	for _, sec := range dashboardSections {
		if !before[sec.capability] {
			continue
		}
		if err = s.section(c, tx, sec.name, now, &out); err != nil {
			return Dashboard{}, err
		}
		out.Sections = append(out.Sections, sec.name)
	}
	if err = tx.Commit(c); err != nil {
		return Dashboard{}, err
	}
	after, err := s.held(c, p, codes)
	if err != nil {
		return Dashboard{}, err
	}
	// 读取期间权限集合变化：已经缓冲在内存里的这批数字不得交出去。
	// 不做「把丢掉的那段摘掉再返回」——那会让调用者拿到一份自己也说不清是什么时刻的首页。
	if !sameSet(before, after) {
		return Dashboard{}, httpx.NewError(httpx.ErrForbidden, "读取期间查看权限发生变化")
	}
	return out, nil
}

func (s *DashboardService) section(ctx context.Context, tx pgx.Tx, name string, now time.Time, out *Dashboard) error {
	switch name {
	case "users":
		byStatus, err := countByStatus(ctx, tx, `SELECT status,count(*)::text FROM users GROUP BY status`)
		if err != nil {
			return err
		}
		var devices string
		if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM devices WHERE status='ACTIVE'`).Scan(&devices); err != nil {
			return err
		}
		// 设备记录不是在线会话：ACTIVE 只表示这条记录还没被撤销。
		out.Users = &DashboardUsers{ByStatus: byStatus, DevicesActive: devices}
	case "nodes":
		byStatus, err := countByStatus(ctx, tx, `SELECT status,count(*)::text FROM nodes GROUP BY status`)
		if err != nil {
			return err
		}
		// ONLINE 是登记状态，不代表此刻心跳新鲜——要看新鲜度去 GET /nodes。
		out.Nodes = &DashboardNodes{ByStatus: byStatus}
	case "traffic":
		to := now.UTC().Truncate(24 * time.Hour)
		from := to.AddDate(0, 0, -(dashboardTrafficDays - 1))
		var up, down, total string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(upload_bytes),0)::text,
 coalesce(sum(download_bytes),0)::text,
 coalesce(sum(upload_bytes)+sum(download_bytes),0)::text
 FROM traffic_daily WHERE stat_date>=$1::date AND stat_date<=$2::date`,
			from, to).Scan(&up, &down, &total); err != nil {
			return err
		}
		out.Traffic = &DashboardTraffic{WindowDays: dashboardTrafficDays,
			From: from.Format(time.DateOnly), To: to.Format(time.DateOnly),
			UploadBytes: up, DownloadBytes: down, TotalBytes: total}
	case "finance":
		var orders, exceptions string
		if err := tx.QueryRow(ctx, `SELECT count(*)::text FROM orders
 WHERE created_at>=$1::date AND created_at<$1::date+1`, now.UTC().Truncate(24*time.Hour)).
			Scan(&orders); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::text FROM deposit_exceptions
 WHERE status='OPEN'`).Scan(&exceptions); err != nil {
			return err
		}
		// 「今天」按 UTC 自然日切，与流量统计同一把尺子；不跟随浏览器时区。
		out.Finance = &DashboardFinance{OrdersCreatedToday: orders, DepositExceptionsOpen: exceptions}
	case "invite":
		var review string
		if err := tx.QueryRow(ctx, `SELECT count(*)::text FROM invite_rewards
 WHERE status='REVIEW'`).Scan(&review); err != nil {
			return err
		}
		// REVIEW 是等人处置的那一批（见第 18 节），PENDING 归 worker，不算待办。
		out.Invite = &DashboardInvite{RewardsInReview: review}
	case "site":
		site, err := loadSiteStats(ctx, tx, now)
		if err != nil {
			return err
		}
		out.Site = site
	default:
		return httpx.Internal
	}
	return nil
}

// countByStatus 把 status→count 读成 map。
//
// 不硬编码状态枚举：数据库加了一档而这里没跟上时，漏掉的那档会凭空消失，
// 首页上的几个数字加起来就对不上总数，而没人会去核对。
func countByStatus(ctx context.Context, tx pgx.Tx, query string) (map[string]string, error) {
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	keys := make([]string, 0, 4)
	for rows.Next() {
		var status, count string
		if err = rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
		keys = append(keys, status)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return out, nil
}

func loadSiteStats(ctx context.Context, tx pgx.Tx, now time.Time) (*DashboardSite, error) {
	w := sitestats.KLWindows(now)
	pageToday, err := sitestats.MetricSum(ctx, tx, "pageview", w.Today, w.Today)
	if err != nil {
		return nil, err
	}
	pageAll, err := sitestats.MetricSum(ctx, tx, "pageview", time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	var visitorsToday, visitorsAll string
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM site_visitor_days WHERE day=$1`, w.Today).Scan(&visitorsToday); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)::text FROM site_visitors`).Scan(&visitorsAll); err != nil {
		return nil, err
	}
	android, err := siteDownload(ctx, tx, "download_android", w)
	if err != nil {
		return nil, err
	}
	windows, err := siteDownload(ctx, tx, "download_windows", w)
	if err != nil {
		return nil, err
	}
	legacy, err := siteDownload(ctx, tx, "download_android_legacy", w)
	if err != nil {
		return nil, err
	}
	return &DashboardSite{
		Timezone:  w.LocationName,
		Pageviews: DashboardSiteWindow{Today: pageToday, History: pageAll},
		Visitors:  DashboardSiteWindow{Today: visitorsToday, History: visitorsAll},
		Downloads: DashboardSiteDownloads{Android: android, Windows: windows, AndroidLegacy: legacy},
	}, nil
}

func siteDownload(ctx context.Context, tx pgx.Tx, metric string, w sitestats.Windows) (DashboardSiteDownload, error) {
	var out DashboardSiteDownload
	var err error
	if out.Today, err = sitestats.MetricSum(ctx, tx, metric, w.Today, w.Today); err != nil {
		return out, err
	}
	if out.Week, err = sitestats.MetricSum(ctx, tx, metric, w.WeekStart, w.Today); err != nil {
		return out, err
	}
	if out.Month, err = sitestats.MetricSum(ctx, tx, metric, w.MonthStart, w.Today); err != nil {
		return out, err
	}
	if out.PreviousMonth, err = sitestats.MetricSum(ctx, tx, metric, w.PreviousStart, w.PreviousEnd); err != nil {
		return out, err
	}
	out.History, err = sitestats.MetricSum(ctx, tx, metric, time.Time{}, time.Time{})
	return out, err
}
