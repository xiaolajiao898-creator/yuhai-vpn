package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/fieldenc"
)

// 字段加密的 AAD：表.列.主键。密文搬到别的行会解密失败。
func secretAAD(id int64) []byte { return fieldenc.AAD("egress_endpoints", "secret_enc", id) }

// SubscriptionAAD 是订阅地址密文的 AAD。
func SubscriptionAAD(id int64) []byte { return fieldenc.AAD("egress_subscriptions", "url_enc", id) }

// Row 状态常量。
const (
	StatusEnabled  = "ENABLED"
	StatusDisabled = "DISABLED"
)

// SealSecret 加密凭据。空凭据返回 nil（例如无认证的 SOCKS5）。
func SealSecret(c *fieldenc.Cipher, id int64, secret map[string]string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	if c == nil {
		return nil, errors.New("egress: 缺少字段加密密钥")
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return c.Encrypt(raw, secretAAD(id))
}

// OpenSecret 解密凭据。
func OpenSecret(c *fieldenc.Cipher, id int64, blob []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(blob) == 0 {
		return out, nil
	}
	if c == nil {
		return nil, errors.New("egress: 缺少字段加密密钥")
	}
	raw, err := c.Decrypt(blob, secretAAD(id))
	if err != nil {
		return nil, errors.New("egress: 凭据解密失败")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("egress: 凭据格式损坏")
	}
	return out, nil
}

// ImportOptions 控制一次导入落库的行为。
type ImportOptions struct {
	Enable         bool   // 导入后直接启用；默认停用，留给管理员核对
	UpdateExisting bool   // 已存在（指纹相同）时更新凭据/参数/名称；否则跳过
	MinTier        int    // 1–9
	Source         string // MANUAL | IMPORT | SUBSCRIPTION
	SubscriptionID int64
}

// ImportResult 是落库结果；Rows 只含脱敏信息。
type ImportResult struct {
	Created  int         `json:"created"`
	Updated  int         `json:"updated"`
	Skipped  int         `json:"skipped"`
	Failed   int         `json:"failed"`
	Disabled int         `json:"disabled"`
	IDs      []string    `json:"ids"`
	Rows     []ResultRow `json:"rows"`
}

type ResultRow struct {
	Line   int    `json:"line"`
	Source string `json:"source"`
	Result string `json:"result"` // CREATED | UPDATED | SKIPPED_EXISTING | SKIPPED_DUPLICATE | FAILED
	ID     string `json:"id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Existing 查询指纹是否已登记（预览用），返回 指纹→id。
func Existing(ctx context.Context, tx pgx.Tx, fps []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(fps) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT fingerprint,id FROM egress_endpoints WHERE fingerprint = ANY($1::text[])`, fps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fp string
		var id int64
		if err := rows.Scan(&fp, &id); err != nil {
			return nil, err
		}
		out[fp] = id
	}
	return out, rows.Err()
}

// Import 把预览通过的行写入库。调用方负责事务、权限与审计。
func Import(ctx context.Context, tx pgx.Tx, c *fieldenc.Cipher, rows []Row, o ImportOptions, now time.Time) (ImportResult, error) {
	res := ImportResult{IDs: []string{}, Rows: []ResultRow{}}
	if o.MinTier < 1 || o.MinTier > 9 {
		return res, fmt.Errorf("min_tier 必须在 1–9")
	}
	switch o.Source {
	case "MANUAL", "IMPORT":
		if o.SubscriptionID != 0 {
			return res, fmt.Errorf("非订阅导入不能带订阅编号")
		}
	case "SUBSCRIPTION":
		if o.SubscriptionID <= 0 {
			return res, fmt.Errorf("订阅导入缺少订阅编号")
		}
	default:
		return res, fmt.Errorf("来源无效")
	}
	status := StatusDisabled
	if o.Enable {
		status = StatusEnabled
	}
	var subID *int64
	if o.SubscriptionID > 0 {
		subID = &o.SubscriptionID
	}
	for _, r := range rows {
		rr := ResultRow{Line: r.Line, Source: r.Source}
		switch {
		case r.Duplicate:
			rr.Result, rr.Error = "SKIPPED_DUPLICATE", r.Error
			res.Skipped++
			res.Rows = append(res.Rows, rr)
			continue
		case r.Endpoint == nil:
			rr.Result, rr.Error = "FAILED", r.Error
			res.Failed++
			res.Rows = append(res.Rows, rr)
			continue
		}
		e := r.Endpoint
		params, err := json.Marshal(e.Params)
		if err != nil {
			return res, err
		}
		kind := "THIRD_PARTY"
		if e.Params["source_kind"] == "SELF_HOSTED" {
			kind = "SELF_HOSTED"
		}
		fp := e.Fingerprint()
		var id int64
		err = tx.QueryRow(ctx, `SELECT id FROM egress_endpoints WHERE fingerprint=$1 FOR UPDATE`, fp).Scan(&id)
		switch {
		case err == nil:
			if !o.UpdateExisting {
				rr.Result, rr.ID = "SKIPPED_EXISTING", "e"+strconv.FormatInt(id, 10)
				res.Skipped++
				res.Rows = append(res.Rows, rr)
				continue
			}
			sealed, serr := SealSecret(c, id, e.Secret)
			if serr != nil {
				return res, serr
			}
			// 更新只动内容字段；启停状态、等级、排序保持管理员的原设置（订阅刷新除外，见调用方）。
			if _, err = tx.Exec(ctx, `UPDATE egress_endpoints SET name=$2,country_code=NULLIF($3,''),city=$4,region=$5,
 supports_udp=$6,params=$7,secret_enc=$8,identity_hint=$9,updated_at=$10 WHERE id=$1`,
				id, e.Name, e.CountryCode, e.City, e.Region, e.SupportsUDP, params, sealed, e.MaskedIdentity(), now); err != nil {
				return res, err
			}
			rr.Result, rr.ID = "UPDATED", "e"+strconv.FormatInt(id, 10)
			res.Updated++
		case errors.Is(err, pgx.ErrNoRows):
			if err = tx.QueryRow(ctx, `INSERT INTO egress_endpoints(name,protocol,host,port,country_code,city,region,kind,
 supports_udp,params,identity_hint,fingerprint,min_tier,status,source,subscription_id,created_at,updated_at)
 VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$17) RETURNING id`,
				e.Name, e.Protocol, e.Host, e.Port, e.CountryCode, e.City, e.Region, kind, e.SupportsUDP, params,
				e.MaskedIdentity(), fp, o.MinTier, status, o.Source, subID, now).Scan(&id); err != nil {
				return res, err
			}
			sealed, serr := SealSecret(c, id, e.Secret)
			if serr != nil {
				return res, serr
			}
			if sealed != nil {
				if _, err = tx.Exec(ctx, `UPDATE egress_endpoints SET secret_enc=$2 WHERE id=$1`, id, sealed); err != nil {
					return res, err
				}
			}
			rr.Result, rr.ID = "CREATED", "e"+strconv.FormatInt(id, 10)
			res.Created++
		default:
			return res, err
		}
		res.IDs = append(res.IDs, rr.ID)
		res.Rows = append(res.Rows, rr)
	}
	return res, nil
}

// Stored 是从库里读出、已解密的出口（仅供下发配置使用，绝不直接序列化给后台列表）。
type Stored struct {
	ID       int64
	Status   string
	MinTier  int
	Endpoint Endpoint
}

// LoadForConnect 读取并解密一条出口，重新走一遍 Validate（库里内容也不被无条件信任）。
func LoadForConnect(ctx context.Context, tx pgx.Tx, c *fieldenc.Cipher, id int64) (Stored, error) {
	var s Stored
	var params []byte
	var blob []byte
	var country *string
	err := tx.QueryRow(ctx, `SELECT id,status,min_tier,name,protocol,host,port,country_code,city,region,supports_udp,params,secret_enc
 FROM egress_endpoints WHERE id=$1`, id).Scan(&s.ID, &s.Status, &s.MinTier, &s.Endpoint.Name, &s.Endpoint.Protocol,
		&s.Endpoint.Host, &s.Endpoint.Port, &country, &s.Endpoint.City, &s.Endpoint.Region, &s.Endpoint.SupportsUDP, &params, &blob)
	if err != nil {
		return s, err
	}
	if country != nil {
		s.Endpoint.CountryCode = strings.TrimSpace(*country)
	}
	if err := json.Unmarshal(params, &s.Endpoint.Params); err != nil {
		return s, errors.New("egress: 参数损坏")
	}
	sec, err := OpenSecret(c, id, blob)
	if err != nil {
		return s, err
	}
	s.Endpoint.Secret = sec
	if err := s.Endpoint.Validate(); err != nil {
		return s, fmt.Errorf("egress: 已登记内容不再有效：%w", err)
	}
	return s, nil
}
