package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/invite"
)

// 系统参数修改（admin.yaml PATCH /configs/{key}）。
//
// system_configs 此前**没有任何接口能写**：改一个充值下限要连库执行 UPDATE。
// 而它被三处业务实时读取，手改的两种典型翻车方式都不会报错、只会静默失效：
//
//  1. **写了代码根本不读的键。** 读取方是 `order.configInt` 与 `invite.loadRewardPlan`，
//     它们各自只认自己那几个键名；拼错一个字母，UPDATE 成功、参数永远不生效。
//  2. **写了读了也用不了的值。** 邀请奖励取的套餐编号必须指向一个 ON_SALE 且类型匹配的套餐，
//     否则 `loadRewardPlan` 返回 nil——奖励不报错，只是**不发**。
//
// 因此这里的规矩与第 17 节黑名单 effects 一致：**只接受代码真正会读的键**，
// 并且按键逐个校验值。白名单之外一律 40001，不写库。
//
// 另一处容易被忽略的：`topup.min_micro` 与 `topup.max_micro` 是两个独立的键，
// 但它们是一对。单独改一个足以把区间改成空集，而下单只会报「金额不在范围内」，
// 没人会想到是配置写反了。因此改任一个都要在同一事务里拿另一个的现值一起校验。
type ConfigWriteService struct{ Security *ApprovalService }

const actionConfigWrite = "configs.write"

// 配置值的大小上限。这些键存的都是一个数字或一个短编号，4KB 已经极宽松；
// 设上限是因为 value 会原样进审计数据。
const maxConfigValueBytes = 4096

// configKey 描述一个允许修改的键。
//
// id 是这个键在幂等表里的 target_id：那套骨架按数字编号记 target，而配置的目标是字符串。
// 编号一旦分配就不能改（改了会让历史幂等记录指向别的键），新增键往后排。
type configKey struct {
	id       int64
	kind     string // int | plan_code
	rewardOf string // kind=plan_code 时，这个编号服务于哪类奖励（DAYS / TRAFFIC）
	min, max int64  // kind=int 时的闭区间
	desc     string
}

// configKeys 是全部允许修改的键。
//
// 每一项都必须有真实的读取方——这张表不是「配置中心」，而是「代码确实会读的那几个开关」。
// 加键之前先确认读取方存在，否则就是在给运维制造一个写了不生效的按钮。
var configKeys = map[string]configKey{
	// internal/order/order.go: cooldown()——下单冷却，同时决定到账迟付的判定窗口。
	"order.cooldown_seconds": {id: 1, kind: "int", min: 60, max: 7 * 24 * 3600,
		desc: "下单冷却秒数"},
	// internal/order/order.go: topupRange()
	"topup.min_micro": {id: 2, kind: "int", min: 1, max: 1_000_000_000_000,
		desc: "充值金额下限（micro）"},
	"topup.max_micro": {id: 3, kind: "int", min: 1, max: 1_000_000_000_000,
		desc: "充值金额上限（micro）"},
	// internal/invite/rewards.go: loadRewardPlan()
	"invite.reward_default_plan_code": {id: 4, kind: "plan_code", rewardOf: "DAYS",
		desc: "时长类邀请奖励使用的套餐编号"},
	"invite.reward_traffic_plan_code": {id: 5, kind: "plan_code", rewardOf: "TRAFFIC",
		desc: "流量类邀请奖励使用的套餐编号"},
	// internal/trial: Enabled()。false 时不再发放新的 24 小时试用。
	"trial.enabled": {id: 6, kind: "bool", desc: "是否向新账户发放 24 小时免费试用"},
}

type ConfigUpdateInput struct {
	OperationID string          `json:"operation_id"`
	Reason      string          `json:"reason"`
	Value       json.RawMessage `json:"value"`
}

type ManagedConfig struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Version   string          `json:"version"`
	Created   bool            `json:"created"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// Update 修改一个系统参数。
//
// 语义是整值替换而不是合并：这些键存的是标量，没有「部分更新」可言。
// 键不存在时（invite.reward_* 两个键种子里就没有）写入第一条而不是 404——
// 对一个代码确实会读、只是还没配过的参数返回「不存在」，只会让人去连库 INSERT。
func (s *ConfigWriteService) Update(ctx context.Context, p ApprovalProof, key string, in ConfigUpdateInput) (
	out ManagedConfig, err error) {
	if s == nil || s.Security == nil || s.Security.Auth == nil {
		return out, httpx.Internal
	}
	spec, ok := configKeys[key]
	if !ok {
		// 不在白名单里的键一律拒绝，**包括数据库里已经存在的那些**。
		// 能写一个没有读取方的键，等于给运维一个「改了但不生效」的按钮。
		return out, httpx.NewError(httpx.ErrBadRequest, "不支持修改该系统参数")
	}
	if len(in.Value) == 0 || len(in.Value) > maxConfigValueBytes {
		return out, httpx.NewError(httpx.ErrBadRequest, "value 缺失或过大")
	}
	// 落库的是解析后重新序列化的那份。
	//
	// 说明一句免得被当成一道真实防线：value 列是 jsonb，空白与键序本来就会被数据库规范化，
	// 因此在「严格拒绝类型不符」之后，这一步与直接存原始字节在库里是同一个结果。
	// 保留它是为了让进入审计快照与响应的值与库内容同源，不是因为它挡住了什么——
	// 挡住类型错误的是下面的解析，不是这次重新序列化。
	canonical, number, text, err := canonicalConfigValue(spec, in.Value)
	if err != nil {
		return out, err
	}

	raw, err := securedMutation(ctx, s.Security, "admin_config_mutations", "admin-config-write",
		p, actionConfigWrite, "system_config", spec.id, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			if e := validateConfigValue(c, tx, key, spec, number, text); e != nil {
				return 0, nil, nil, nil, e
			}
			var prev json.RawMessage
			var prevVersion int
			e := tx.QueryRow(c, `SELECT value,version FROM system_configs WHERE key=$1`, key).
				Scan(&prev, &prevVersion)
			existed := true
			if errors.Is(e, pgx.ErrNoRows) {
				existed, e = false, nil
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			entry := ManagedConfig{Key: key, Value: canonical, Created: !existed, UpdatedAt: now}
			var version int
			if e = tx.QueryRow(c, `INSERT INTO system_configs(key,value,version,updated_by,updated_at)
 VALUES($1,$2,1,$3,$4)
 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,version=system_configs.version+1,
 updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at
 RETURNING version`, key, canonical, actor, now).Scan(&version); e != nil {
				return 0, nil, nil, nil, e
			}
			entry.Version = strconv.Itoa(version)
			// 审计快照只接受白名单内的有界字符串字段（见 internal/audit/canonical.go），
			// 因此这里用 code 记键名、version_code 记版本、name 记新值的文本形式。
			//
			// **旧值刻意不进快照**：它可能是本接口上线前手写进去的任意内容，
			// 一旦不符合审计投影的约束，整笔写入就会失败——那会让一个坏值把「修好它」也一起堵死。
			// 旧值的版本号仍然记下来，足以定位是哪一次改动。
			status := "UPDATED"
			if !existed {
				status = "CREATED"
			}
			valueText := string(canonical)
			if spec.kind == "plan_code" {
				valueText = text
			}
			before, _ := json.Marshal(map[string]any{"code": key,
				"version_code": strconv.Itoa(prevVersion),
				"status":       map[bool]string{true: "EXISTS", false: "ABSENT"}[existed]})
			after, _ := json.Marshal(map[string]any{"code": key,
				"version_code": strconv.Itoa(version), "status": status,
				"name": valueText, "decision_note": in.Reason})
			_ = prev
			return spec.id, entry, before, after, nil
		})
	if err != nil {
		return ManagedConfig{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedConfig{}, httpx.Internal
	}
	return out, nil
}

// canonicalConfigValue 按键的类型解析并规范化值，同时做不依赖数据库的那部分校验。
func canonicalConfigValue(spec configKey, in json.RawMessage) (json.RawMessage, int64, string, error) {
	bad := func(msg string) (json.RawMessage, int64, string, error) {
		return nil, 0, "", httpx.NewError(httpx.ErrBadRequest, msg)
	}
	switch spec.kind {
	case "int":
		// 解成 any 而不是直接解成 json.Number：后者会**接受 JSON 字符串**
		// （`"3600"` 也能解进 json.Number），等于悄悄做了一次类型转换。
		// 这里要的是「不是数字就拒绝」，不是「能凑出数字就行」。
		var v any
		dec := json.NewDecoder(bytes.NewReader(in))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			return bad(spec.desc + "必须是整数")
		}
		if dec.More() {
			return bad("value 必须是单个值")
		}
		n, ok := v.(json.Number)
		if !ok {
			return bad(spec.desc + "必须是整数")
		}
		// 用 json.Number 而不是 float64：float64 到 2^53 之后就开始丢精度，
		// 而 micro 金额远超这个范围。小数点写法在这里一律拒绝，不做四舍五入。
		num, e := strconv.ParseInt(n.String(), 10, 64)
		if e != nil {
			return bad(spec.desc + "必须是整数")
		}
		if num < spec.min || num > spec.max {
			return bad(spec.desc + "超出允许范围 " + strconv.FormatInt(spec.min, 10) + "–" +
				strconv.FormatInt(spec.max, 10))
		}
		return json.RawMessage(strconv.FormatInt(num, 10)), num, "", nil
	case "plan_code":
		var code string
		if json.Unmarshal(in, &code) != nil {
			return bad(spec.desc + "必须是字符串")
		}
		if !nodeText(code, 32, false) {
			return bad(spec.desc + "必须是 1–32 个可见字符")
		}
		canonical, e := json.Marshal(code)
		if e != nil {
			return nil, 0, "", httpx.Internal
		}
		return canonical, 0, code, nil
	case "bool":
		var on bool
		dec := json.NewDecoder(bytes.NewReader(in))
		if dec.Decode(&on) != nil || dec.More() {
			return bad(spec.desc + "必须是布尔值")
		}
		if on {
			return json.RawMessage("true"), 1, "true", nil
		}
		return json.RawMessage("false"), 0, "false", nil
	}
	return nil, 0, "", httpx.Internal
}

// validateConfigValue 做需要读库的那部分校验，在写入的同一事务里跑。
func validateConfigValue(ctx context.Context, tx pgx.Tx, key string, spec configKey,
	number int64, text string) error {
	switch spec.kind {
	case "int":
		// 充值上下限是一对：单独改一个足以把区间改成空集。
		other := ""
		switch key {
		case "topup.min_micro":
			other = "topup.max_micro"
		case "topup.max_micro":
			other = "topup.min_micro"
		}
		if other == "" {
			return nil
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT value FROM system_configs WHERE key=$1`, other).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			// 另一端还没配过：读取方 configInt 取不到会退回内置默认值，
			// 此时没有「一对」可言，放行。
			return nil
		}
		if err != nil {
			return err
		}
		var peer int64
		if json.Unmarshal(raw, &peer) != nil {
			// 另一端的现值不是整数（只可能是本接口上线前手写进去的）：
			// 报出来而不是假装校验过了。
			return httpx.NewError(httpx.ErrConflict, other+" 的现值不是整数，请先修正它")
		}
		if key == "topup.min_micro" && number > peer ||
			key == "topup.max_micro" && number < peer {
			return httpx.NewError(httpx.ErrBadRequest, "充值下限不得大于上限（另一端现值 "+
				strconv.FormatInt(peer, 10)+"）")
		}
		return nil
	case "plan_code":
		// 用与发放侧完全相同的一套判断：套餐必须存在、ON_SALE、类型匹配且本身合法。
		// 不一致的编号写进去不会报错，只会在发奖励时静默跳过。
		plan, err := invite.LookupRewardPlan(ctx, tx, spec.rewardOf, text)
		if err != nil {
			return err
		}
		if plan == nil {
			return httpx.NewError(httpx.ErrBadRequest,
				"该套餐编号不存在、已下架或类型不符，写进去奖励会静默不发")
		}
		return nil
	case "bool":
		return nil
	}
	return httpx.Internal
}
