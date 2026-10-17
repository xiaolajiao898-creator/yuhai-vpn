package admin

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// nodeReadPermission 与 node_read 的授权查询共用同一个字面量，
// 总览按能力分段时需要按名字引用它。
const nodeReadPermission = "nodes.read"

type NodeReadService struct{ Auth *Service }
type ManagedNodeGroup struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	MinTier int    `json:"min_tier"`
}
type ManagedNode struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Group           ManagedNodeGroup `json:"group"`
	CountryCode     string           `json:"country_code"`
	City            string           `json:"city"`
	Region          string           `json:"region"`
	Status          string           `json:"status"`
	IsRecommended   bool             `json:"is_recommended"`
	CapacityUsers   int              `json:"capacity_users"`
	OnlineCount     int              `json:"online_count"`
	LoadScore       int              `json:"load_score"`
	LastHeartbeatAt *time.Time       `json:"last_heartbeat_at"`
	HeartbeatState  string           `json:"heartbeat_state"`
	AgentVersion    *string          `json:"agent_version"`
	UpdatedAt       time.Time        `json:"updated_at"`
}
type ManagedNodeFilter struct {
	Page, PageSize              int
	Search, Status, CountryCode string
}
type ManagedNodePage struct {
	Items      []ManagedNode `json:"items"`
	Total      string        `json:"total"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	ObservedAt time.Time     `json:"observed_at"`
}
type ManagedInbound struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
}
type ManagedNodeDetail struct {
	Node       ManagedNode      `json:"node"`
	Inbounds   []ManagedInbound `json:"inbounds"`
	ObservedAt time.Time        `json:"observed_at"`
	SyncStatus string           `json:"sync_status"`
}

var nodeCountryCode = regexp.MustCompile(`^[A-Z]{2}$`)

func nodeText(value string, limit int, empty bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit || !empty && strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validNodeStatus(value string) bool {
	return value == "ONLINE" || value == "OFFLINE" || value == "MAINTENANCE" || value == "DISABLED"
}
func normalizeManagedNodeFilter(f ManagedNodeFilter) (ManagedNodeFilter, error) {
	var err error
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return f, err
	}
	if f.Search != "" && (!nodeText(f.Search, 64, false) || strings.TrimSpace(f.Search) != f.Search) || f.Status != "" && !validNodeStatus(f.Status) || f.CountryCode != "" && !nodeCountryCode.MatchString(f.CountryCode) {
		return f, httpx.NewError(httpx.ErrBadRequest, "节点筛选参数无效")
	}
	return f, nil
}
func nodeHeartbeat(last *time.Time, now time.Time) string {
	if last == nil {
		return "NEVER"
	}
	if last.After(now) {
		return "CLOCK_SKEW"
	}
	if now.Sub(*last) > 90*time.Second {
		return "STALE"
	}
	return "FRESH"
}

// A private read-only snapshot is bracketed by fresh authority checks. A
// permission/session revoked during a query cannot disclose the buffered rows.
func (s *NodeReadService) read(ctx context.Context, p Principal, fn func(context.Context, pgx.Tx, time.Time) error) error {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil {
		return httpx.Internal
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.authority(c, p); err != nil {
		return err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollbackApproval(tx)
	if err = fn(c, tx, s.Auth.Clock.Now()); err != nil {
		return err
	}
	if err = tx.Commit(c); err != nil {
		return err
	}
	return s.authority(c, p)
}
func (s *NodeReadService) authority(ctx context.Context, p Principal) error {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return errSession
	}
	var allowed bool
	err = s.Auth.Pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM admin_users u JOIN admin_sessions session ON session.admin_user_id=u.id
 WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3)
 AND session.id=$2 AND session.revoked_at IS NULL AND session.expires_at>$3 AND session.last_active_at>$3-interval '30 minutes'
 AND EXISTS(SELECT 1 FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id JOIN admin_permissions perm ON perm.code=rp.permission_code WHERE ur.admin_user_id=u.id AND perm.code='nodes.read' AND perm.risk_level=0))`, id, p.SessionID, s.Auth.Clock.Now()).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return httpx.NewError(httpx.ErrForbidden, "当前会话或节点查看权限已失效")
	}
	return nil
}

const managedNodeColumns = `n.id::text,n.name,g.id::text,g.name,g.min_tier,n.country_code,n.city,n.region,n.status,n.is_recommended,n.capacity_users,n.online_count,n.load_score,n.last_heartbeat_at,n.agent_version,n.updated_at`
const managedNodeJoin = ` FROM nodes n JOIN node_groups g ON g.id=n.group_id`

func scanManagedNode(row pgx.Row, now time.Time) (n ManagedNode, err error) {
	err = row.Scan(&n.ID, &n.Name, &n.Group.ID, &n.Group.Name, &n.Group.MinTier, &n.CountryCode, &n.City, &n.Region, &n.Status, &n.IsRecommended, &n.CapacityUsers, &n.OnlineCount, &n.LoadScore, &n.LastHeartbeatAt, &n.AgentVersion, &n.UpdatedAt)
	if err != nil {
		return n, err
	}
	if _, err = positiveApprovalID(n.ID); err != nil {
		return ManagedNode{}, httpx.Internal
	}
	if _, err = positiveApprovalID(n.Group.ID); err != nil {
		return ManagedNode{}, httpx.Internal
	}
	if !nodeText(n.Name, 64, false) || !nodeText(n.Group.Name, 64, false) || n.Group.MinTier < 0 || n.Group.MinTier > 9 || !nodeCountryCode.MatchString(n.CountryCode) || !nodeText(n.City, 64, true) || !nodeText(n.Region, 32, false) || !validNodeStatus(n.Status) || n.CapacityUsers < 0 || n.OnlineCount < 0 || n.LoadScore < 0 || n.LoadScore > 100 || n.AgentVersion != nil && !nodeText(*n.AgentVersion, 32, true) {
		return ManagedNode{}, httpx.Internal
	}
	n.HeartbeatState = nodeHeartbeat(n.LastHeartbeatAt, now)
	return n, nil
}
func (s *NodeReadService) List(ctx context.Context, p Principal, f ManagedNodeFilter) (out ManagedNodePage, err error) {
	f, err = normalizeManagedNodeFilter(f)
	if err != nil {
		return out, err
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` WHERE ($1='' OR strpos(lower(n.name),lower($1))>0) AND ($2='' OR n.status=$2) AND ($3='' OR n.country_code=$3)`
		var total int64
		if err := tx.QueryRow(c, `SELECT count(*)`+managedNodeJoin+where, f.Search, f.Status, f.CountryCode).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(c, `SELECT `+managedNodeColumns+managedNodeJoin+where+` ORDER BY n.sort_order,n.id LIMIT $4 OFFSET $5`, f.Search, f.Status, f.CountryCode, f.PageSize, (f.Page-1)*f.PageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = ManagedNodePage{Items: []ManagedNode{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			n, err := scanManagedNode(rows, now)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, n)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedNodePage{}, err
	}
	return out, nil
}
func (s *NodeReadService) Get(ctx context.Context, p Principal, id int64) (out ManagedNodeDetail, err error) {
	if id <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "节点编号无效")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		n, err := scanManagedNode(tx.QueryRow(c, `SELECT `+managedNodeColumns+managedNodeJoin+` WHERE n.id=$1`, id), now)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "节点不存在")
		}
		if err != nil {
			return err
		}
		out = ManagedNodeDetail{Node: n, Inbounds: []ManagedInbound{}, ObservedAt: now, SyncStatus: "UNAVAILABLE"}
		rows, err := tx.Query(c, `SELECT id::text,protocol,port,priority,status FROM node_inbounds WHERE node_id=$1 ORDER BY priority,id LIMIT 101`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v ManagedInbound
			if err := rows.Scan(&v.ID, &v.Protocol, &v.Port, &v.Priority, &v.Status); err != nil {
				return err
			}
			if _, err := positiveApprovalID(v.ID); err != nil {
				return httpx.Internal
			}
			switch v.Protocol {
			case "VLESS_REALITY", "TROJAN", "SS2022", "WIREGUARD", "HYSTERIA2":
			default:
				return httpx.Internal
			}
			if v.Port < 1 || v.Port > 65535 || (v.Status != "ENABLED" && v.Status != "DISABLED") || len(out.Inbounds) >= 100 {
				return httpx.Internal
			}
			out.Inbounds = append(out.Inbounds, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedNodeDetail{}, err
	}
	return out, nil
}
