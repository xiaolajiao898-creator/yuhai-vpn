package admin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/money"
)

// 套餐管理：列表、新建、修改、上下架（方案 11.4）。
//
// 在这之前套餐只能靠手写 SQL 改库——而套餐是全系统**唯一一个既决定用户买到什么、
// 又决定收多少钱**的对象。
//
// 全文最要紧的一条规矩：**任何影响「买到什么 / 付多少钱」的改动都必须让 version + 1。**
//
// 理由在 `internal/plan/plan.go` 的 `LockQuote` 里：未使用的报价（plan_quotes）记着报价当时的
// plan_version，下单时拿它与套餐当前版本比对，不一致就让用户重新报价。也就是说
// **version 是保护未结报价的那道闸**。改了价却不 +1，10 分钟内已经拿到旧报价的用户
// 就会按旧价格买到改过的套餐——差价是真金白银。因此本文件把「实质字段」与「外观字段」
// 分得很清楚，并由测试两个方向都钉住。
//
// 另外三条边界：
//
//   - **code 与 plan_type 不可修改。** code 是 App 与订单快照里引用的稳定标识；
//     plan_type 一改，已经引用该套餐的订阅与订单就对不上了——那是一个新套餐，不是改。
//   - **新建的套餐恒为 DRAFT。** 上架是另一个动作、另一个权限，不能在新建时顺手完成。
//   - **下架不影响已购用户。** 订阅带着自己的 tier / device_limit / 到期时间，不回查套餐。
//     下架只是不再卖，且会让未结报价失效（LockQuote 同时校验 status='ON_SALE'）。
type PlanReadService struct{ Auth *Service }

type PlanWriteService struct{ Security *ApprovalService }

const (
	planRead         = "plans.read"
	actionPlanCreate = "plans.create"
	actionPlanUpdate = "plans.update"
	actionPlanStatus = "plans.status"
)

// 字节数一律用字符串：套餐配额可以轻松超过 2^53，JSON number 在浏览器里会失精。
type ManagedPlan struct {
	ID               string    `json:"id"`
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	PlanType         string    `json:"plan_type"`
	Tier             int       `json:"tier"`
	DurationDays     *int      `json:"duration_days"`
	PeriodQuotaBytes *string   `json:"period_quota_bytes"`
	PackBytes        *string   `json:"pack_bytes"`
	ValidityDays     *int      `json:"validity_days"`
	DeviceLimit      *int      `json:"device_limit"`
	ExtraDevices     *int      `json:"extra_devices"`
	Price            string    `json:"price"`
	Badge            *string   `json:"badge"`
	SortOrder        int       `json:"sort_order"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ManagedPlanList struct {
	Items      []ManagedPlan `json:"items"`
	ObservedAt time.Time     `json:"observed_at"`
}

type PlanCreate struct {
	OperationID      string  `json:"operation_id"`
	Reason           string  `json:"reason"`
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	PlanType         string  `json:"plan_type"`
	Tier             int     `json:"tier"`
	DurationDays     *int    `json:"duration_days"`
	PeriodQuotaBytes *string `json:"period_quota_bytes"`
	PackBytes        *string `json:"pack_bytes"`
	ValidityDays     *int    `json:"validity_days"`
	DeviceLimit      *int    `json:"device_limit"`
	ExtraDevices     *int    `json:"extra_devices"`
	Price            string  `json:"price"`
	Badge            *string `json:"badge"`
	SortOrder        int     `json:"sort_order"`
}

// PlanPatch 是**真正的部分更新**：没出现的字段保持原样。
// period_quota_bytes 出现且为 null 表示「改成不限量」——这与「没提这个字段」是两件事，
// 因此本接口靠请求体里实际出现的键名来区分，而不是靠零值猜。
type PlanPatch struct {
	OperationID      string  `json:"operation_id"`
	Reason           string  `json:"reason"`
	Name             *string `json:"name"`
	Tier             *int    `json:"tier"`
	DurationDays     *int    `json:"duration_days"`
	PeriodQuotaBytes *string `json:"period_quota_bytes"`
	PackBytes        *string `json:"pack_bytes"`
	ValidityDays     *int    `json:"validity_days"`
	DeviceLimit      *int    `json:"device_limit"`
	ExtraDevices     *int    `json:"extra_devices"`
	Price            *string `json:"price"`
	Badge            *string `json:"badge"`
	SortOrder        *int    `json:"sort_order"`
}

type PlanStatusInput struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

var planCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

var planTypes = map[string]bool{"TIME": true, "TRAFFIC": true, "DEVICE_ADDON": true}

// 上架、下架、再上架都可以；**回到 DRAFT 不行**——DRAFT 的含义是「从未售卖过」，
// 而一个上过架的套餐可能已经有报价和订单引用它，把它说成草稿是不诚实的。
var planStatusMoves = map[string]map[string]bool{
	"DRAFT":    {"ON_SALE": true, "OFF_SALE": true},
	"ON_SALE":  {"OFF_SALE": true},
	"OFF_SALE": {"ON_SALE": true},
}

// planShape 是一份套餐的实质内容：它决定用户买到什么、付多少钱。
// 这几个字段里任何一个变化都必须让 version + 1（见文件开头）。
// name / badge / sort_order 不在其中——它们只影响展示，不影响成交内容。
type planShape struct {
	Tier                            int
	Duration, Validity, Devices     int
	Extra                           int
	Quota                           *int64
	Pack                            *int64
	Price                           int64
	PlanType, Code, Name, Status    string
	Badge                           *string
	Sort, Version                   int
	ID                              int64
	CreatedAt, UpdatedAt            time.Time
	hasDuration, hasValidity, hasDL bool
	hasExtra                        bool
}

// substantive 只包含实质字段，用于判断要不要 +1。
func (p planShape) substantive() planShape {
	return planShape{Tier: p.Tier, Duration: p.Duration, Validity: p.Validity, Devices: p.Devices,
		Extra: p.Extra, Quota: p.Quota, Pack: p.Pack, Price: p.Price,
		hasDuration: p.hasDuration, hasValidity: p.hasValidity, hasDL: p.hasDL, hasExtra: p.hasExtra}
}

func samePointer(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (p planShape) sameSubstance(q planShape) bool {
	a, b := p.substantive(), q.substantive()
	return a.Tier == b.Tier && a.Duration == b.Duration && a.Validity == b.Validity &&
		a.Devices == b.Devices && a.Extra == b.Extra && a.Price == b.Price &&
		samePointer(a.Quota, b.Quota) && samePointer(a.Pack, b.Pack) &&
		a.hasDuration == b.hasDuration && a.hasValidity == b.hasValidity &&
		a.hasDL == b.hasDL && a.hasExtra == b.hasExtra
}

// ---------- 校验 ----------

// planBytes 解析字节数字符串。上限取 2^53-1 与流量链路一致：
// 再大的配额没有业务含义，却会让任何用 JSON number 的下游失精。
func planBytes(raw string) (int64, error) {
	if raw == "" || len(raw) > 19 || strings.TrimSpace(raw) != raw {
		return 0, httpx.NewError(httpx.ErrBadRequest, "字节数必须是 1 到 9007199254740991 之间的整数字符串")
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 || v > 1<<53-1 || strconv.FormatInt(v, 10) != raw {
		return 0, httpx.NewError(httpx.ErrBadRequest, "字节数必须是 1 到 9007199254740991 之间的整数字符串")
	}
	return v, nil
}

func planPrice(raw string) (int64, error) {
	v, err := money.Parse(raw)
	// 数据库约束要求价格是 0.1 USDT 的整数倍；在这里就说清楚，不要等约束报错。
	if err != nil || v <= 0 || int64(v)%100000 != 0 {
		return 0, httpx.NewError(httpx.ErrBadRequest, "价格必须是大于 0 且为 0.1 USDT 整数倍的金额字符串")
	}
	return int64(v), nil
}

func planCount(v int, limit int, label string) error {
	if v <= 0 || v > limit {
		return httpx.NewError(httpx.ErrBadRequest, label+"超出允许范围")
	}
	return nil
}

// validateShape 按套餐类型校验形状，并且**拒绝不属于该类型的字段**。
// 静默忽略一个填错位置的字段，等于让运维以为自己设置生效了。
func validateShape(s *planShape) error {
	switch s.PlanType {
	case "TIME":
		if !s.hasDuration || !s.hasDL || s.hasValidity || s.hasExtra || s.Pack != nil {
			return httpx.NewError(httpx.ErrBadRequest, "TIME 套餐必须且只能给出 duration_days、device_limit，可选 period_quota_bytes")
		}
		if err := planCount(s.Duration, 3650, "duration_days"); err != nil {
			return err
		}
		return planCount(s.Devices, 100, "device_limit")
	case "TRAFFIC":
		if s.Pack == nil || !s.hasValidity || !s.hasDL || s.hasDuration || s.hasExtra || s.Quota != nil {
			return httpx.NewError(httpx.ErrBadRequest, "TRAFFIC 套餐必须且只能给出 pack_bytes、validity_days、device_limit")
		}
		if err := planCount(s.Validity, 3650, "validity_days"); err != nil {
			return err
		}
		return planCount(s.Devices, 100, "device_limit")
	default: // DEVICE_ADDON
		if !s.hasExtra || s.hasDuration || s.hasValidity || s.hasDL || s.Quota != nil || s.Pack != nil {
			return httpx.NewError(httpx.ErrBadRequest, "DEVICE_ADDON 套餐必须且只能给出 extra_devices")
		}
		return planCount(s.Extra, 100, "extra_devices")
	}
}

// ---------- 只读 ----------

// 不带表别名：同一份列清单要同时用在 SELECT、INSERT ... RETURNING 与
// UPDATE ... RETURNING 三处，而后两者没有别名可用。
const planColumns = `id::text,code,name,plan_type,tier,duration_days,period_quota_bytes,
 pack_bytes,validity_days,device_limit,extra_devices,price_micro,
 display_config->>'badge',sort_order,status,version,created_at,updated_at`

func scanManagedPlan(row pgx.Row) (ManagedPlan, error) {
	var v ManagedPlan
	var quota, pack *int64
	var price int64
	if err := row.Scan(&v.ID, &v.Code, &v.Name, &v.PlanType, &v.Tier, &v.DurationDays, &quota, &pack,
		&v.ValidityDays, &v.DeviceLimit, &v.ExtraDevices, &price, &v.Badge, &v.SortOrder,
		&v.Status, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return ManagedPlan{}, err
	}
	if quota != nil {
		s := strconv.FormatInt(*quota, 10)
		v.PeriodQuotaBytes = &s
	}
	if pack != nil {
		s := strconv.FormatInt(*pack, 10)
		v.PackBytes = &s
	}
	v.Price = amount(price)
	return v, nil
}

// List 返回**全部**套餐，包括草稿与已下架的——后台要看的正是 App 看不到的那些。
func (s *PlanReadService) List(ctx context.Context, p Principal, status string) (out ManagedPlanList, err error) {
	if status != "" && planStatusMoves[status] == nil {
		return out, httpx.NewError(httpx.ErrBadRequest, "套餐状态无效")
	}
	read := &FinanceReadService{Auth: s.Auth}
	err = read.readWith(ctx, p, planRead, func(c context.Context, tx pgx.Tx, now time.Time) error {
		rows, e := tx.Query(c, `SELECT `+planColumns+` FROM plans WHERE ($1=''OR status=$1)
 ORDER BY sort_order,id`, status)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedPlanList{Items: []ManagedPlan{}, ObservedAt: now}
		for rows.Next() {
			v, e := scanManagedPlan(rows)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedPlanList{}, err
	}
	return out, nil
}

// ---------- 写 ----------

func (s *PlanWriteService) write(ctx context.Context, p ApprovalProof, action string, targetID int64,
	operationID, reason string,
	fn func(context.Context, pgx.Tx, int64, time.Time) (int64, any, json.RawMessage, json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, httpx.Internal
	}
	return securedMutation(ctx, s.Security, "admin_plan_mutations", "admin-plan-write",
		p, action, "plan", targetID, operationID, reason, fn)
}

func planConflict(e error, msg string) error {
	var pg *pgconn.PgError
	if errors.As(e, &pg) && pg.Code == "23505" {
		return httpx.NewError(httpx.ErrConflict, msg)
	}
	return e
}

func planAudit(id int64, code, name, status, note string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(id, 10),
		"plan_code": code, "name": name, "status": status, "decision_note": note})
	return raw
}

// Create 新建套餐。**恒为 DRAFT**：上架是另一个动作、另一个权限。
func (s *PlanWriteService) Create(ctx context.Context, p ApprovalProof, in PlanCreate) (out ManagedPlan, err error) {
	if !planCodePattern.MatchString(in.Code) {
		return out, httpx.NewError(httpx.ErrBadRequest, "code 必须是 2–32 位大写字母、数字或下划线，且以字母开头")
	}
	if !nodeText(in.Name, 64, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "名称必须是 1–64 个可见字符")
	}
	if !planTypes[in.PlanType] {
		return out, httpx.NewError(httpx.ErrBadRequest, "套餐类型无效")
	}
	if in.Tier < 1 || in.Tier > 9 {
		return out, httpx.NewError(httpx.ErrBadRequest, "tier 必须在 1–9 之间")
	}
	if in.SortOrder < 0 || in.SortOrder > 100000 {
		return out, httpx.NewError(httpx.ErrBadRequest, "sort_order 超出允许范围")
	}
	if in.Badge != nil && !nodeText(*in.Badge, 32, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "角标必须是 1–32 个可见字符")
	}
	shape := planShape{PlanType: in.PlanType, Tier: in.Tier}
	if shape.Price, err = planPrice(in.Price); err != nil {
		return out, err
	}
	if err = applyInts(&shape, in.DurationDays, in.ValidityDays, in.DeviceLimit, in.ExtraDevices); err != nil {
		return out, err
	}
	if shape.Quota, err = optionalBytes(in.PeriodQuotaBytes); err != nil {
		return out, err
	}
	if shape.Pack, err = optionalBytes(in.PackBytes); err != nil {
		return out, err
	}
	if err = validateShape(&shape); err != nil {
		return out, err
	}

	raw, err := s.write(ctx, p, actionPlanCreate, 0, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			row := tx.QueryRow(c, `INSERT INTO plans(code,name,plan_type,tier,duration_days,
 period_quota_bytes,pack_bytes,validity_days,device_limit,extra_devices,price_micro,display_config,
 sort_order,status,version,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,jsonb_build_object('badge',$12::text),$13,'DRAFT',1,$14,$14)
 RETURNING `+planColumns, in.Code, in.Name, in.PlanType, in.Tier,
				planColumn(shape.hasDuration, shape.Duration), shape.Quota, shape.Pack,
				planColumn(shape.hasValidity, shape.Validity), planColumn(shape.hasDL, shape.Devices),
				planColumn(shape.hasExtra, shape.Extra), shape.Price, in.Badge, in.SortOrder, now)
			plan, e := scanManagedPlan(row)
			if e != nil {
				return 0, nil, nil, nil, planConflict(e, "该 code 已被占用")
			}
			id, e := positiveApprovalID(plan.ID)
			if e != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			return id, plan, nil, planAudit(id, plan.Code, plan.Name, plan.Status, in.Reason), nil
		})
	if err != nil {
		return ManagedPlan{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedPlan{}, httpx.Internal
	}
	return out, nil
}

// planColumn 把「这个类型不该有的字段」写成 NULL——数据库的 chk_plan_shape
// 要求不属于该类型的列必须为空，不能留着上一次的值。
func planColumn(present bool, v int) *int {
	if !present {
		return nil
	}
	return &v
}

func optionalBytes(raw *string) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := planBytes(*raw)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func applyInts(s *planShape, duration, validity, devices, extra *int) error {
	if duration != nil {
		s.Duration, s.hasDuration = *duration, true
	}
	if validity != nil {
		s.Validity, s.hasValidity = *validity, true
	}
	if devices != nil {
		s.Devices, s.hasDL = *devices, true
	}
	if extra != nil {
		s.Extra, s.hasExtra = *extra, true
	}
	return nil
}

// presentKeys 取出请求体里**实际出现过**的顶层键名。
// 区分「没提这个字段」与「显式给了 null」只能靠它——period_quota_bytes 的
// 「不限量」正是用显式 null 表达的。请求体的合法性已由 decodeApprovalObject 保证。
func presentKeys(raw []byte) map[string]bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// Update 部分更新。code 与 plan_type 不可改；实质字段变化时 version + 1。
func (s *PlanWriteService) Update(ctx context.Context, p ApprovalProof, planID int64, in PlanPatch,
	body []byte) (out ManagedPlan, err error) {
	if planID <= 0 {
		return out, httpx.NewError(httpx.ErrBadRequest, "套餐编号无效")
	}
	keys := presentKeys(body)
	changeable := false
	for _, k := range []string{"name", "tier", "duration_days", "period_quota_bytes", "pack_bytes",
		"validity_days", "device_limit", "extra_devices", "price", "badge", "sort_order"} {
		changeable = changeable || keys[k]
	}
	if !changeable {
		return out, httpx.NewError(httpx.ErrBadRequest, "没有给出任何要修改的字段")
	}
	if in.Name != nil && !nodeText(*in.Name, 64, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "名称必须是 1–64 个可见字符")
	}
	if in.Tier != nil && (*in.Tier < 1 || *in.Tier > 9) {
		return out, httpx.NewError(httpx.ErrBadRequest, "tier 必须在 1–9 之间")
	}
	if in.SortOrder != nil && (*in.SortOrder < 0 || *in.SortOrder > 100000) {
		return out, httpx.NewError(httpx.ErrBadRequest, "sort_order 超出允许范围")
	}
	if in.Badge != nil && !nodeText(*in.Badge, 32, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "角标必须是 1–32 个可见字符")
	}

	raw, err := s.write(ctx, p, actionPlanUpdate, planID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			current, e := lockPlan(c, tx, planID)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			next := current
			if in.Name != nil {
				next.Name = *in.Name
			}
			if in.Tier != nil {
				next.Tier = *in.Tier
			}
			if in.DurationDays != nil {
				next.Duration, next.hasDuration = *in.DurationDays, true
			}
			if in.ValidityDays != nil {
				next.Validity, next.hasValidity = *in.ValidityDays, true
			}
			if in.DeviceLimit != nil {
				next.Devices, next.hasDL = *in.DeviceLimit, true
			}
			if in.ExtraDevices != nil {
				next.Extra, next.hasExtra = *in.ExtraDevices, true
			}
			// 显式 null 表示「改成不限量」；键没出现才是保持原样。
			if keys["period_quota_bytes"] {
				if next.Quota, e = optionalBytes(in.PeriodQuotaBytes); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			if keys["pack_bytes"] {
				if next.Pack, e = optionalBytes(in.PackBytes); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			if in.Price != nil {
				if next.Price, e = planPrice(*in.Price); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			if e = validateShape(&next); e != nil {
				return 0, nil, nil, nil, e
			}

			// 这一行就是本文件的要点：实质变了才 +1，只改外观不 +1。
			// +1 会让所有未使用的旧报价作废（LockQuote 比对 plan_version）。
			version := current.Version
			if !current.sameSubstance(next) {
				version++
			}
			badge := in.Badge
			if !keys["badge"] {
				badge = current.Badge
			}
			sort := current.Sort
			if in.SortOrder != nil {
				sort = *in.SortOrder
			}
			// 只改 badge 这一个键：display_config 按设计还要放描述与分组，
			// 整个覆盖会把别人写进去的东西抹掉。
			row := tx.QueryRow(c, `UPDATE plans SET name=$2,tier=$3,duration_days=$4,
 period_quota_bytes=$5,pack_bytes=$6,validity_days=$7,device_limit=$8,extra_devices=$9,
 price_micro=$10,display_config=jsonb_set(COALESCE(display_config,'{}'::jsonb),'{badge}',
 COALESCE(to_jsonb($11::text),'null'::jsonb),true),sort_order=$12,version=$13,updated_at=$14
 WHERE id=$1 RETURNING `+planColumns, planID, next.Name, next.Tier,
				planColumn(next.hasDuration, next.Duration), next.Quota, next.Pack,
				planColumn(next.hasValidity, next.Validity), planColumn(next.hasDL, next.Devices),
				planColumn(next.hasExtra, next.Extra), next.Price, badge, sort, version, now)
			plan, e := scanManagedPlan(row)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			before := planAudit(planID, current.Code, current.Name, current.Status, in.Reason)
			return planID, plan, before, planAudit(planID, plan.Code, plan.Name, plan.Status, in.Reason), nil
		})
	if err != nil {
		return ManagedPlan{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedPlan{}, httpx.Internal
	}
	return out, nil
}

// SetStatus 上架 / 下架。**不改 version**：LockQuote 另行校验 status='ON_SALE'，
// 下架本身已经让未结报价失效，不需要再动版本号。
func (s *PlanWriteService) SetStatus(ctx context.Context, p ApprovalProof, planID int64, in PlanStatusInput) error {
	if planID <= 0 {
		return httpx.NewError(httpx.ErrBadRequest, "套餐编号无效")
	}
	if planStatusMoves[in.Status] == nil || in.Status == "DRAFT" {
		return httpx.NewError(httpx.ErrBadRequest, "目标状态只能是 ON_SALE 或 OFF_SALE")
	}
	_, err := s.write(ctx, p, actionPlanStatus, planID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			current, e := lockPlan(c, tx, planID)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			if current.Status != in.Status {
				if !planStatusMoves[current.Status][in.Status] {
					return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict,
						"不允许从 "+current.Status+" 变更为 "+in.Status)
				}
				if _, e = tx.Exec(c, `UPDATE plans SET status=$2,updated_at=$3 WHERE id=$1`,
					planID, in.Status, now); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			before := planAudit(planID, current.Code, current.Name, current.Status, in.Reason)
			return planID, struct{}{}, before, planAudit(planID, current.Code, current.Name, in.Status, in.Reason), nil
		})
	return err
}

func lockPlan(ctx context.Context, tx pgx.Tx, id int64) (planShape, error) {
	var s planShape
	var duration, validity, devices, extra *int
	err := tx.QueryRow(ctx, `SELECT code,name,plan_type,tier,duration_days,period_quota_bytes,
 pack_bytes,validity_days,device_limit,extra_devices,price_micro,display_config->>'badge',
 sort_order,status,version FROM plans WHERE id=$1 FOR UPDATE`, id).
		Scan(&s.Code, &s.Name, &s.PlanType, &s.Tier, &duration, &s.Quota, &s.Pack, &validity,
			&devices, &extra, &s.Price, &s.Badge, &s.Sort, &s.Status, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, httpx.NewError(httpx.ErrNotFound, "套餐不存在")
	}
	if err != nil {
		return s, err
	}
	s.ID = id
	if duration != nil {
		s.Duration, s.hasDuration = *duration, true
	}
	if validity != nil {
		s.Validity, s.hasValidity = *validity, true
	}
	if devices != nil {
		s.Devices, s.hasDL = *devices, true
	}
	if extra != nil {
		s.Extra, s.hasExtra = *extra, true
	}
	return s, nil
}
