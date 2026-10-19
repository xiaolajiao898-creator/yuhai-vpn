package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/egress"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 出口管理（外购代理 / 自建多协议节点）后台接口。
//
// 与节点写接口同一套骨架：写前核验权限 → operation_id 串行与幂等（request_hash 防复用）→
// 执行 → 记录幂等与审计 → 提交 → 写后再核验权限。另有三条本域特有的边界：
//
//  1. 凭据只进不出：任何响应、审计快照、错误信息里都不出现账号密码/UUID/口令/订阅地址，
//     只给 identity_hint（如 "ab***"）与订阅主机名。
//  2. 预览不落库：先预览逐行报错，再提交；提交时服务端重新解析同一份输入，不信任预览结果。
//  3. 订阅抓取在事务外进行，带 SSRF 防护（egress.Fetcher），抓取失败不改动任何已登记出口。

type SubscriptionFetcher interface {
	Fetch(ctx context.Context, raw string) (string, error)
}

type EgressService struct {
	Auth    *Service
	Audit   *audit.Service
	Cipher  *fieldenc.Cipher
	Fetcher SubscriptionFetcher
}

const (
	permEgressRead  = "egress.read"
	permEgressWrite = "egress.write"

	actionEgressImport     = "egress.import"
	actionEgressUpdate     = "egress.update"
	actionEgressSubCreate  = "egress.subscription_create"
	actionEgressSubRefresh = "egress.subscription_refresh"
)

func badEgress(msg string) error { return httpx.NewError(httpx.ErrBadRequest, msg) }

func (s *EgressService) ready() bool {
	return s != nil && s.Auth != nil && s.Auth.Pool != nil && s.Auth.Clock != nil && s.Audit != nil && s.Cipher != nil
}

func (s *EgressService) authority(ctx context.Context, p Principal, permission string, risk int) (int64, error) {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return 0, errSession
	}
	var allowed bool
	err = s.Auth.Pool.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM admin_users u JOIN admin_sessions session ON session.admin_user_id=u.id
 WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3)
 AND session.id=$2 AND session.revoked_at IS NULL AND session.expires_at>$3 AND session.last_active_at>$3-interval '30 minutes'
 AND EXISTS(SELECT 1 FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id
   JOIN admin_permissions perm ON perm.code=rp.permission_code
   WHERE ur.admin_user_id=u.id AND perm.code=$4 AND perm.risk_level=$5))`,
		id, p.SessionID, s.Auth.Clock.Now(), permission, risk).Scan(&allowed)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, httpx.NewError(httpx.ErrForbidden, "当前会话或出口管理权限已失效")
	}
	return id, nil
}

func (s *EgressService) read(ctx context.Context, p Principal, permission string, risk int, fn func(context.Context, pgx.Tx, time.Time) error) error {
	if !s.ready() {
		return httpx.Internal
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if _, err := s.authority(c, p, permission, risk); err != nil {
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
	_, err = s.authority(c, p, permission, risk)
	return err
}

func (s *EgressService) write(ctx context.Context, p ApprovalProof, action, operationID string,
	fn func(context.Context, pgx.Tx, int64, time.Time) (int64, any, error)) (json.RawMessage, error) {
	if !s.ready() {
		return nil, httpx.Internal
	}
	if !validOperationID(operationID) {
		return nil, badEgress("operation_id 必须是 UUIDv4")
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	actor, err := s.authority(c, p.Principal, permEgressWrite, 1)
	if err != nil {
		return nil, err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer rollbackApproval(tx)
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "admin-egress-write:"+operationID); err != nil {
		return nil, err
	}
	var priorAction, priorHash string
	var priorResponse json.RawMessage
	err = tx.QueryRow(c, `SELECT action_code,request_hash,response FROM admin_egress_mutations WHERE operation_id=$1`,
		operationID).Scan(&priorAction, &priorHash, &priorResponse)
	switch {
	case err == nil:
		if priorAction != action || priorHash != p.RequestHash {
			return nil, httpx.NewError(httpx.ErrConflict, "同一 operation_id 已用于不同的请求")
		}
		return priorResponse, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	// 所有出口写入串行：导入与订阅刷新都按指纹 upsert，并发执行会互相踩。
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended('admin-egress-write-global',0))`); err != nil {
		return nil, err
	}
	now := s.Auth.Clock.Now()
	targetID, out, err := fn(c, tx, actor, now)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(c, `INSERT INTO admin_egress_mutations(admin_user_id,operation_id,action_code,request_hash,target_id,response,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, operationID, action, p.RequestHash, targetID, raw, now); err != nil {
		return nil, err
	}
	after, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(targetID, 10), "action_code": action})
	if err = s.Audit.Append(c, tx, audit.Event{AdminUserID: &actor, ActionCode: action, TargetType: "egress",
		TargetID: &targetID, RequestID: p.RequestID, IP: p.IP, UserAgent: p.UserAgent, Result: "SUCCESS",
		AfterData: after}); err != nil {
		return nil, err
	}
	if err = tx.Commit(c); err != nil {
		return nil, err
	}
	if _, err = s.authority(c, p.Principal, permEgressWrite, 1); err != nil {
		return nil, err
	}
	return raw, nil
}

// ---------- 列表 ----------

type EgressItem struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Protocol       string            `json:"protocol"`
	Host           string            `json:"host"`
	Port           int               `json:"port"`
	CountryCode    string            `json:"country_code"`
	City           string            `json:"city"`
	Region         string            `json:"region"`
	Kind           string            `json:"kind"`
	SupportsUDP    bool              `json:"supports_udp"`
	CapacityUsers  int               `json:"capacity_users"`
	IdentityHint   string            `json:"identity_hint"`
	Params         map[string]string `json:"params"`
	MinTier        int               `json:"min_tier"`
	Status         string            `json:"status"`
	SortOrder      int               `json:"sort_order"`
	Source         string            `json:"source"`
	SubscriptionID *string           `json:"subscription_id"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type EgressList struct {
	Items      []EgressItem `json:"items"`
	Total      string       `json:"total"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
	ObservedAt time.Time    `json:"observed_at"`
}

type EgressFilter struct {
	Page, PageSize                int
	Status, Protocol, CountryCode string
	Search                        string
}

func (s *EgressService) List(ctx context.Context, p Principal, f EgressFilter) (out EgressList, err error) {
	if f.Status != "" && f.Status != egress.StatusEnabled && f.Status != egress.StatusDisabled {
		return out, badEgress("status 只能是 ENABLED 或 DISABLED")
	}
	if f.Protocol != "" && !egress.Supported[f.Protocol] {
		return out, badEgress("protocol 无效")
	}
	f.CountryCode = strings.ToUpper(f.CountryCode)
	if f.CountryCode != "" && (len(f.CountryCode) != 2 || strings.Trim(f.CountryCode, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "") {
		return out, badEgress("country_code 必须是两位字母")
	}
	if len([]rune(f.Search)) > 64 {
		return out, badEgress("搜索词过长")
	}
	err = s.read(ctx, p, permEgressRead, 0, func(c context.Context, tx pgx.Tx, now time.Time) error {
		where := `($1='' OR status=$1) AND ($2='' OR protocol=$2) AND ($3='' OR country_code=$3)
 AND ($4='' OR strpos(lower(name),lower($4))>0 OR strpos(host,lower($4))>0)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*) FROM egress_endpoints WHERE `+where, f.Status, f.Protocol, f.CountryCode, f.Search).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT id,name,protocol,host,port,COALESCE(country_code,''),city,region,kind,supports_udp,capacity_users,
 identity_hint,params,min_tier,status,sort_order,source,subscription_id::text,created_at,updated_at
 FROM egress_endpoints WHERE `+where+` ORDER BY min_tier,sort_order,id LIMIT $5 OFFSET $6`,
			f.Status, f.Protocol, f.CountryCode, f.Search, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = EgressList{Items: []EgressItem{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var it EgressItem
			var id int64
			var params []byte
			if e = rows.Scan(&id, &it.Name, &it.Protocol, &it.Host, &it.Port, &it.CountryCode, &it.City, &it.Region, &it.Kind,
				&it.SupportsUDP, &it.CapacityUsers, &it.IdentityHint, &params, &it.MinTier, &it.Status, &it.SortOrder, &it.Source,
				&it.SubscriptionID, &it.CreatedAt, &it.UpdatedAt); e != nil {
				return e
			}
			it.ID = "e" + strconv.FormatInt(id, 10)
			it.CountryCode = strings.TrimSpace(it.CountryCode)
			if e = json.Unmarshal(params, &it.Params); e != nil {
				return httpx.Internal
			}
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	return out, err
}

// ---------- 导入 ----------

type EgressImportInput struct {
	OperationID     string `json:"operation_id"`
	Text            string `json:"text"`
	DefaultProtocol string `json:"default_protocol"`
	DefaultUDP      bool   `json:"default_udp"`
	CountryCode     string `json:"country_code"`
	City            string `json:"city"`
	Region          string `json:"region"`
	NamePrefix      string `json:"name_prefix"`
	MinTier         *int   `json:"min_tier"`
	Enable          bool   `json:"enable"`
	UpdateExisting  bool   `json:"update_existing"`
}

type PreviewRow struct {
	Line        int    `json:"line"`
	Source      string `json:"source"`
	Protocol    string `json:"protocol,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Name        string `json:"name,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	SupportsUDP bool   `json:"supports_udp"`
	Error       string `json:"error,omitempty"`
	Duplicate   bool   `json:"duplicate,omitempty"`
	ExistingID  string `json:"existing_id,omitempty"`
}

type PreviewSummary struct {
	Total     int `json:"total"`
	Valid     int `json:"valid"`
	Invalid   int `json:"invalid"`
	Duplicate int `json:"duplicate"`
	Existing  int `json:"existing"`
}

type PreviewResult struct {
	Rows    []PreviewRow   `json:"rows"`
	Summary PreviewSummary `json:"summary"`
}

func (in EgressImportInput) options() egress.ParseOptions {
	return egress.ParseOptions{DefaultProtocol: strings.ToUpper(in.DefaultProtocol), DefaultUDP: in.DefaultUDP,
		CountryCode: in.CountryCode, City: in.City, Region: in.Region, NamePrefix: in.NamePrefix}
}

func parseImport(text string, o egress.ParseOptions) ([]egress.Row, error) {
	rows, err := egress.Parse(text, o)
	if err != nil {
		return nil, badEgress(err.Error())
	}
	return rows, nil
}

func previewOf(rows []egress.Row, existing map[string]int64) PreviewResult {
	out := PreviewResult{Rows: make([]PreviewRow, 0, len(rows))}
	for _, r := range rows {
		pr := PreviewRow{Line: r.Line, Source: r.Source, Error: r.Error, Duplicate: r.Duplicate}
		out.Summary.Total++
		switch {
		case r.Duplicate:
			out.Summary.Duplicate++
		case r.Endpoint == nil:
			out.Summary.Invalid++
		default:
			e := r.Endpoint
			pr.Protocol, pr.Host, pr.Port, pr.Name, pr.CountryCode, pr.SupportsUDP = e.Protocol, e.Host, e.Port, e.Name, e.CountryCode, e.SupportsUDP
			out.Summary.Valid++
			if id, ok := existing[e.Fingerprint()]; ok {
				pr.ExistingID = "e" + strconv.FormatInt(id, 10)
				out.Summary.Existing++
			}
		}
		out.Rows = append(out.Rows, pr)
	}
	return out
}

func fingerprints(rows []egress.Row) []string {
	fps := []string{}
	for _, r := range rows {
		if r.Endpoint != nil && !r.Duplicate {
			fps = append(fps, r.Endpoint.Fingerprint())
		}
	}
	return fps
}

// Preview 解析并标出每行结果与库里已存在的条目，不落库。需要 egress.write（输入里本就含凭据）。
func (s *EgressService) Preview(ctx context.Context, p Principal, in EgressImportInput) (out PreviewResult, err error) {
	rows, err := parseImport(in.Text, in.options())
	if err != nil {
		return out, err
	}
	err = s.read(ctx, p, permEgressWrite, 1, func(c context.Context, tx pgx.Tx, _ time.Time) error {
		existing, e := egress.Existing(c, tx, fingerprints(rows))
		if e != nil {
			return e
		}
		out = previewOf(rows, existing)
		return nil
	})
	return out, err
}

func minTierOf(p *int) (int, error) {
	if p == nil {
		return 1, nil
	}
	if *p < 1 || *p > 9 {
		return 0, badEgress("min_tier 必须在 1–9")
	}
	return *p, nil
}

func (s *EgressService) Import(ctx context.Context, p ApprovalProof, in EgressImportInput) (json.RawMessage, error) {
	tier, err := minTierOf(in.MinTier)
	if err != nil {
		return nil, err
	}
	rows, err := parseImport(in.Text, in.options())
	if err != nil {
		return nil, err
	}
	return s.write(ctx, p, actionEgressImport, in.OperationID, func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, error) {
		res, err := egress.Import(c, tx, s.Cipher, rows, egress.ImportOptions{Enable: in.Enable, UpdateExisting: in.UpdateExisting,
			MinTier: tier, Source: "IMPORT"}, now)
		if err != nil {
			return 0, nil, err
		}
		// 审计目标：本批第一条新建/更新的出口；全部失败时记到操作者本人（审计要求正整数目标）。
		target := actor
		for _, id := range res.IDs {
			if n, e := strconv.ParseInt(strings.TrimPrefix(id, "e"), 10, 64); e == nil && n > 0 {
				target = n
				break
			}
		}
		return target, res, nil
	})
}

// ---------- 修改 ----------

type EgressUpdate struct {
	OperationID   string  `json:"operation_id"`
	Name          *string `json:"name"`
	Status        *string `json:"status"`
	CountryCode   *string `json:"country_code"`
	City          *string `json:"city"`
	Region        *string `json:"region"`
	MinTier       *int    `json:"min_tier"`
	SortOrder     *int    `json:"sort_order"`
	SupportsUDP   *bool   `json:"supports_udp"`
	CapacityUsers *int    `json:"capacity_users"`
}

func (s *EgressService) Update(ctx context.Context, p ApprovalProof, rawID string, in EgressUpdate) (json.RawMessage, error) {
	id, err := parseEgressID(rawID)
	if err != nil {
		return nil, err
	}
	if in.Name == nil && in.Status == nil && in.CountryCode == nil && in.City == nil && in.Region == nil &&
		in.MinTier == nil && in.SortOrder == nil && in.SupportsUDP == nil && in.CapacityUsers == nil {
		return nil, badEgress("没有要修改的字段")
	}
	if in.Status != nil && *in.Status != egress.StatusEnabled && *in.Status != egress.StatusDisabled {
		return nil, badEgress("status 只能是 ENABLED 或 DISABLED")
	}
	if in.MinTier != nil && (*in.MinTier < 1 || *in.MinTier > 9) {
		return nil, badEgress("min_tier 必须在 1–9")
	}
	if in.SortOrder != nil && (*in.SortOrder < -100000 || *in.SortOrder > 100000) {
		return nil, badEgress("sort_order 超出范围")
	}
	if in.CapacityUsers != nil && (*in.CapacityUsers < 0 || *in.CapacityUsers > 1000000) {
		return nil, badEgress("capacity_users 必须在 0–1000000，0 表示不设上限")
	}
	return s.write(ctx, p, actionEgressUpdate, in.OperationID, func(c context.Context, tx pgx.Tx, _ int64, now time.Time) (int64, any, error) {
		st, err := egress.LoadForConnect(c, tx, s.Cipher, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpx.NewError(httpx.ErrNotFound, "出口不存在")
		}
		if err != nil {
			return 0, nil, httpx.NewError(httpx.ErrConflict, "该出口已登记内容无效，请重新导入覆盖")
		}
		e := st.Endpoint
		if in.Name != nil {
			e.Name = *in.Name
		}
		if in.CountryCode != nil {
			e.CountryCode = *in.CountryCode
		}
		if in.City != nil {
			e.City = *in.City
		}
		if in.Region != nil {
			e.Region = *in.Region
		}
		if in.SupportsUDP != nil {
			if *in.SupportsUDP && e.Protocol != egress.SOCKS5 {
				return 0, nil, badEgress("只有 SOCKS5 可以手动标注是否支持 UDP")
			}
			e.SupportsUDP = *in.SupportsUDP
		}
		if err := e.Validate(); err != nil {
			return 0, nil, badEgress(err.Error())
		}
		tag, err := tx.Exec(c, `UPDATE egress_endpoints SET name=$2,country_code=NULLIF($3,''),city=$4,region=$5,supports_udp=$6,
 status=COALESCE($7,status),min_tier=COALESCE($8,min_tier),sort_order=COALESCE($9,sort_order),
 capacity_users=COALESCE($10,capacity_users),updated_at=$11 WHERE id=$1`,
			id, e.Name, e.CountryCode, e.City, e.Region, e.SupportsUDP, in.Status, in.MinTier, in.SortOrder, in.CapacityUsers, now)
		if err != nil {
			return 0, nil, err
		}
		if tag.RowsAffected() != 1 {
			return 0, nil, httpx.NewError(httpx.ErrNotFound, "出口不存在")
		}
		item, err := s.item(c, tx, id)
		return id, item, err
	})
}

func (s *EgressService) item(c context.Context, tx pgx.Tx, id int64) (EgressItem, error) {
	var it EgressItem
	var params []byte
	err := tx.QueryRow(c, `SELECT name,protocol,host,port,COALESCE(country_code,''),city,region,kind,supports_udp,capacity_users,
 identity_hint,params,min_tier,status,sort_order,source,subscription_id::text,created_at,updated_at FROM egress_endpoints WHERE id=$1`, id).
		Scan(&it.Name, &it.Protocol, &it.Host, &it.Port, &it.CountryCode, &it.City, &it.Region, &it.Kind, &it.SupportsUDP, &it.CapacityUsers,
			&it.IdentityHint, &params, &it.MinTier, &it.Status, &it.SortOrder, &it.Source, &it.SubscriptionID, &it.CreatedAt, &it.UpdatedAt)
	if err != nil {
		return it, err
	}
	it.ID = "e" + strconv.FormatInt(id, 10)
	it.CountryCode = strings.TrimSpace(it.CountryCode)
	if err := json.Unmarshal(params, &it.Params); err != nil {
		return it, httpx.Internal
	}
	return it, nil
}

func parseEgressID(raw string) (int64, error) {
	if !strings.HasPrefix(raw, "e") {
		return 0, badEgress("出口编号无效")
	}
	id, err := strconv.ParseInt(raw[1:], 10, 64)
	if err != nil || id <= 0 || "e"+strconv.FormatInt(id, 10) != raw {
		return 0, badEgress("出口编号无效")
	}
	return id, nil
}

// ---------- 订阅 ----------

type SubscriptionItem struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	URLHint         string     `json:"url_hint"`
	DefaultProtocol string     `json:"default_protocol"`
	CountryCode     string     `json:"country_code"`
	Region          string     `json:"region"`
	MinTier         int        `json:"min_tier"`
	Status          string     `json:"status"`
	LastFetchedAt   *time.Time `json:"last_fetched_at"`
	LastResult      string     `json:"last_result"`
	Endpoints       int        `json:"endpoints"`
}

type SubscriptionInput struct {
	OperationID     string `json:"operation_id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	DefaultProtocol string `json:"default_protocol"`
	CountryCode     string `json:"country_code"`
	Region          string `json:"region"`
	MinTier         *int   `json:"min_tier"`
}

type SubscriptionRefresh struct {
	OperationID    string `json:"operation_id"`
	Enable         bool   `json:"enable"`
	DisableMissing *bool  `json:"disable_missing"`
}

func (s *EgressService) Subscriptions(ctx context.Context, p Principal) (out []SubscriptionItem, err error) {
	err = s.read(ctx, p, permEgressRead, 0, func(c context.Context, tx pgx.Tx, _ time.Time) error {
		rows, e := tx.Query(c, `SELECT s.id,s.name,s.url_hint,s.default_protocol,COALESCE(s.country_code,''),s.region,s.min_tier,s.status,
 s.last_fetched_at,s.last_result,(SELECT count(*) FROM egress_endpoints x WHERE x.subscription_id=s.id)
 FROM egress_subscriptions s ORDER BY s.id LIMIT 201`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = []SubscriptionItem{}
		for rows.Next() {
			var it SubscriptionItem
			var id int64
			if e = rows.Scan(&id, &it.Name, &it.URLHint, &it.DefaultProtocol, &it.CountryCode, &it.Region, &it.MinTier, &it.Status,
				&it.LastFetchedAt, &it.LastResult, &it.Endpoints); e != nil {
				return e
			}
			it.ID = "s" + strconv.FormatInt(id, 10)
			it.CountryCode = strings.TrimSpace(it.CountryCode)
			out = append(out, it)
		}
		return rows.Err()
	})
	return out, err
}

func subscriptionDefaults(in SubscriptionInput) (egress.ParseOptions, int, error) {
	tier, err := minTierOf(in.MinTier)
	if err != nil {
		return egress.ParseOptions{}, 0, err
	}
	o := egress.ParseOptions{DefaultProtocol: strings.ToUpper(in.DefaultProtocol), CountryCode: strings.ToUpper(in.CountryCode), Region: in.Region}
	if o.DefaultProtocol == "" {
		o.DefaultProtocol = egress.SOCKS5
	}
	switch o.DefaultProtocol {
	case egress.SOCKS5, egress.HTTP, egress.HTTPS:
	default:
		return o, 0, badEgress("默认协议只能是 SOCKS5 / HTTP / HTTPS")
	}
	return o, tier, nil
}

// SubscriptionPreview 抓取并解析订阅，不落库。抓取在任何数据库事务之外进行。
func (s *EgressService) SubscriptionPreview(ctx context.Context, p Principal, in SubscriptionInput) (out PreviewResult, err error) {
	if !s.ready() || s.Fetcher == nil {
		return out, httpx.Internal
	}
	o, _, err := subscriptionDefaults(in)
	if err != nil {
		return out, err
	}
	if _, err := egress.ValidateSubscriptionURL(in.URL); err != nil {
		return out, badEgress(err.Error())
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	if _, err := s.authority(c, p, permEgressWrite, 1); err != nil {
		cancel()
		return out, err
	}
	cancel()
	body, err := s.Fetcher.Fetch(ctx, in.URL)
	if err != nil {
		return out, badEgress(err.Error())
	}
	rows, err := parseImport(body, o)
	if err != nil {
		return out, err
	}
	err = s.read(ctx, p, permEgressWrite, 1, func(c context.Context, tx pgx.Tx, _ time.Time) error {
		existing, e := egress.Existing(c, tx, fingerprints(rows))
		if e != nil {
			return e
		}
		out = previewOf(rows, existing)
		return nil
	})
	return out, err
}

func (s *EgressService) CreateSubscription(ctx context.Context, p ApprovalProof, in SubscriptionInput) (json.RawMessage, error) {
	o, tier, err := subscriptionDefaults(in)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 64 {
		return nil, badEgress("订阅名称必须是 1–64 个字符")
	}
	u, err := egress.ValidateSubscriptionURL(in.URL)
	if err != nil {
		return nil, badEgress(err.Error())
	}
	if o.CountryCode != "" && (len(o.CountryCode) != 2 || strings.Trim(o.CountryCode, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "") {
		return nil, badEgress("country_code 必须是两位字母")
	}
	if len([]rune(in.Region)) > 32 {
		return nil, badEgress("地区过长")
	}
	return s.write(ctx, p, actionEgressSubCreate, in.OperationID, func(c context.Context, tx pgx.Tx, _ int64, now time.Time) (int64, any, error) {
		var id int64
		if err := tx.QueryRow(c, `INSERT INTO egress_subscriptions(name,url_enc,url_hint,default_protocol,country_code,region,min_tier,status,created_at,updated_at)
 VALUES($1,'\x00',$2,$3,NULLIF($4,''),$5,$6,'ENABLED',$7,$7) RETURNING id`, name, u.Hostname(), o.DefaultProtocol, o.CountryCode, in.Region, tier, now).Scan(&id); err != nil {
			return 0, nil, err
		}
		sealed, err := s.Cipher.Encrypt([]byte(in.URL), egress.SubscriptionAAD(id))
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(c, `UPDATE egress_subscriptions SET url_enc=$2 WHERE id=$1`, id, sealed); err != nil {
			return 0, nil, err
		}
		return id, SubscriptionItem{ID: "s" + strconv.FormatInt(id, 10), Name: name, URLHint: u.Hostname(),
			DefaultProtocol: o.DefaultProtocol, CountryCode: o.CountryCode, Region: in.Region, MinTier: tier, Status: "ENABLED"}, nil
	})
}

type subscriptionRow struct {
	id                        int64
	status, protocol, country string
	region                    string
	tier                      int
	url                       string
}

func (s *EgressService) loadSubscription(ctx context.Context, p Principal, id int64) (sub subscriptionRow, err error) {
	err = s.read(ctx, p, permEgressWrite, 1, func(c context.Context, tx pgx.Tx, _ time.Time) error {
		var blob []byte
		var country *string
		e := tx.QueryRow(c, `SELECT id,status,default_protocol,country_code,region,min_tier,url_enc FROM egress_subscriptions WHERE id=$1`, id).
			Scan(&sub.id, &sub.status, &sub.protocol, &country, &sub.region, &sub.tier, &blob)
		if errors.Is(e, pgx.ErrNoRows) {
			return httpx.NewError(httpx.ErrNotFound, "订阅不存在")
		}
		if e != nil {
			return e
		}
		if country != nil {
			sub.country = strings.TrimSpace(*country)
		}
		raw, e := s.Cipher.Decrypt(blob, egress.SubscriptionAAD(id))
		if e != nil {
			return httpx.NewError(httpx.ErrConflict, "订阅地址无法解密，请删除后重建")
		}
		sub.url = string(raw)
		return nil
	})
	return sub, err
}

// RefreshSubscription 抓取订阅并按指纹更新：新条目新建、已有条目更新凭据与参数、
// 本次订阅里消失的条目（disable_missing，默认开）停用而不是删除。抓取失败不改动任何出口。
func (s *EgressService) RefreshSubscription(ctx context.Context, p ApprovalProof, rawID string, in SubscriptionRefresh) (json.RawMessage, error) {
	if !s.ready() || s.Fetcher == nil {
		return nil, httpx.Internal
	}
	if !strings.HasPrefix(rawID, "s") {
		return nil, badEgress("订阅编号无效")
	}
	id, err := strconv.ParseInt(rawID[1:], 10, 64)
	if err != nil || id <= 0 || "s"+strconv.FormatInt(id, 10) != rawID {
		return nil, badEgress("订阅编号无效")
	}
	if !validOperationID(in.OperationID) {
		return nil, badEgress("operation_id 必须是 UUIDv4")
	}
	sub, err := s.loadSubscription(ctx, p.Principal, id)
	if err != nil {
		return nil, err
	}
	if sub.status != "ENABLED" {
		return nil, httpx.NewError(httpx.ErrConflict, "订阅已停用")
	}
	body, fetchErr := s.Fetcher.Fetch(ctx, sub.url)
	var rows []egress.Row
	if fetchErr == nil {
		rows, fetchErr = egress.Parse(body, egress.ParseOptions{DefaultProtocol: sub.protocol, CountryCode: sub.country, Region: sub.region})
	}
	disableMissing := in.DisableMissing == nil || *in.DisableMissing
	return s.write(ctx, p, actionEgressSubRefresh, in.OperationID, func(c context.Context, tx pgx.Tx, _ int64, now time.Time) (int64, any, error) {
		if fetchErr != nil {
			msg := fetchErr.Error()
			if len([]rune(msg)) > 190 {
				msg = string([]rune(msg)[:190])
			}
			if _, err := tx.Exec(c, `UPDATE egress_subscriptions SET last_fetched_at=$2,last_result=$3,updated_at=$2 WHERE id=$1`, id, now, "失败："+msg); err != nil {
				return 0, nil, err
			}
			return id, map[string]any{"ok": false, "error": msg}, nil
		}
		res, err := egress.Import(c, tx, s.Cipher, rows, egress.ImportOptions{Enable: in.Enable, UpdateExisting: true,
			MinTier: sub.tier, Source: "SUBSCRIPTION", SubscriptionID: id}, now)
		if err != nil {
			return 0, nil, err
		}
		if disableMissing {
			keep := fingerprints(rows)
			tag, err := tx.Exec(c, `UPDATE egress_endpoints SET status='DISABLED',updated_at=$3
 WHERE subscription_id=$1 AND status='ENABLED' AND NOT (fingerprint = ANY($2::text[]))`, id, keep, now)
			if err != nil {
				return 0, nil, err
			}
			res.Disabled = int(tag.RowsAffected())
		}
		summary := "成功：新建 " + strconv.Itoa(res.Created) + "，更新 " + strconv.Itoa(res.Updated) + "，失败 " + strconv.Itoa(res.Failed) +
			"，停用 " + strconv.Itoa(res.Disabled)
		if _, err := tx.Exec(c, `UPDATE egress_subscriptions SET last_fetched_at=$2,last_result=$3,updated_at=$2 WHERE id=$1`, id, now, summary); err != nil {
			return 0, nil, err
		}
		return id, map[string]any{"ok": true, "result": res}, nil
	})
}
