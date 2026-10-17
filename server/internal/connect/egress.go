package connect

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/egress"
	"github.com/yuhai-vpn/server/internal/httpx"
)

// 出口（外购代理 / 自建多协议节点）的 App 侧两个接口：
//
//   - GET  /v1/egress           列出出口（不含主机与凭据；等级不够的只给展示信息）
//   - POST /v1/connect/egress   为本设备生成所选出口的完整内核配置（含凭据，no-store）
//
// 与 WireGuard 的 /connect/profiles 同一套权益判定（entitlement）与设备核验（deviceMaterial），
// 不另立标准。出口没有心跳：可用性只由「已启用 + 等级够 + 权益有效」决定，
// 真实连通性由客户端连接结果如实反映，服务端不假装知道。

// EgressView 是 App 列表里的一条出口。不含主机、端口与任何凭据。
type EgressView struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	CountryCode  string  `json:"country_code"`
	City         string  `json:"city"`
	Region       string  `json:"region"`
	Protocol     string  `json:"protocol"`
	SupportsUDP  bool    `json:"supports_udp"`
	Available    bool    `json:"available"`
	LockedReason *string `json:"locked_reason"`
}

// EgressProfile 是下发给设备的连接材料。Config 是交给 Xray 内核的完整 JSON，原样使用。
type EgressProfile struct {
	EgressID    string          `json:"egress_id"`
	Protocol    string          `json:"protocol"`
	SupportsUDP bool            `json:"supports_udp"`
	Core        string          `json:"core"`
	CoreVersion string          `json:"core_version"`
	ExpiresAt   string          `json:"expires_at"`
	Config      json.RawMessage `json:"config"`
	Notice      string          `json:"notice"`
}

// ParseEgressID 接受 "e123" 形式；与 WireGuard 节点的纯数字编号区分开，客户端据此选择引擎。
func ParseEgressID(raw string) (int64, error) {
	if !strings.HasPrefix(raw, "e") {
		return 0, badRequest("出口编号无效")
	}
	id, err := strconv.ParseInt(raw[1:], 10, 64)
	if err != nil || id <= 0 || "e"+strconv.FormatInt(id, 10) != raw {
		return 0, badRequest("出口编号无效")
	}
	return id, nil
}

func (s *Service) readTx(ctx context.Context) (pgx.Tx, func(), error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	return tx, func() {
		c, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = tx.Rollback(c)
	}, nil
}

func userTier(ctx context.Context, tx pgx.Tx, userID int64, now time.Time) (int, error) {
	var tier int
	err := tx.QueryRow(ctx, `SELECT GREATEST(
 COALESCE((SELECT tier FROM subscriptions WHERE user_id=$1 AND expire_at>$2),0),
 COALESCE((SELECT max(tier) FROM traffic_packs WHERE user_id=$1 AND status='ACTIVE' AND expire_at>$2 AND used_bytes<total_bytes),0))
 FROM users WHERE id=$1 AND status='ACTIVE'`, userID, now).Scan(&tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.NewError(httpx.ErrAccountDisabled, "账户不可用")
	}
	return tier, err
}

// EgressList 列出已启用的出口。
func (s *Service) EgressList(ctx context.Context, userID int64) ([]EgressView, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, done, err := s.readTx(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	now := s.Clock.Now()
	tier, err := userTier(ctx, tx, userID, now)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,COALESCE(country_code,''),city,region,protocol,supports_udp,min_tier
 FROM egress_endpoints WHERE status='ENABLED' ORDER BY min_tier,sort_order,id LIMIT 1001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EgressView{}
	for rows.Next() {
		var v EgressView
		var id int64
		var minTier int
		if err := rows.Scan(&id, &v.Name, &v.CountryCode, &v.City, &v.Region, &v.Protocol, &v.SupportsUDP, &minTier); err != nil {
			return nil, err
		}
		if len(out) >= 1000 {
			return nil, httpx.NewError(httpx.ErrNodeUnavailable, "出口列表超出读取上限")
		}
		v.ID = "e" + strconv.FormatInt(id, 10)
		v.CountryCode = strings.TrimSpace(v.CountryCode)
		switch {
		case !egress.Supported[v.Protocol]:
			r := "PROTOCOL_UNSUPPORTED"
			v.LockedReason = &r
		case tier < minTier:
			r := "TIER"
			v.LockedReason = &r
		default:
			v.Available = true
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, tx.Commit(ctx)
}

// EgressConnect 为本设备生成所选出口的内核配置。
func (s *Service) EgressConnect(ctx context.Context, userID, deviceID int64, rawID string) (EgressProfile, error) {
	var out EgressProfile
	if userID <= 0 || deviceID <= 0 {
		return out, httpx.NewError(httpx.ErrTokenMissing, "缺少账户或设备身份")
	}
	id, err := ParseEgressID(rawID)
	if err != nil {
		return out, err
	}
	if s.Cipher == nil {
		return out, httpx.Internal
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, done, err := s.readTx(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	now := s.Clock.Now()
	right, err := entitlement(ctx, tx, userID, now)
	if err != nil {
		return out, err
	}
	// 设备必须属于本人且未被移除；WireGuard 材料在这里用不到，只取核验结果。
	if _, err := deviceMaterial(ctx, tx, userID, deviceID); err != nil {
		return out, err
	}
	st, err := egress.LoadForConnect(ctx, tx, s.Cipher, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "出口不存在或已停用")
	}
	if err != nil {
		// 内容损坏或解密失败：不把半份配置交出去。
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "出口资料无效，请换一个出口")
	}
	switch {
	case st.Status != egress.StatusEnabled:
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "出口不存在或已停用")
	case right.tier < st.MinTier:
		return out, httpx.NewError(httpx.ErrTierTooLow, "当前套餐等级不能使用该出口")
	}
	cfg, err := st.Endpoint.ClientConfig(egress.ClientOptions{Mode: egress.ModeTUN})
	if err != nil {
		return out, httpx.NewError(httpx.ErrNodeUnavailable, "出口资料无效，请换一个出口")
	}
	if err := tx.Commit(ctx); err != nil {
		return out, err
	}
	notice := ""
	if !st.Endpoint.SupportsUDP {
		notice = "该出口不支持 UDP：网页、聊天可用；部分游戏、语音通话与 QUIC 会改走 TCP 或不可用。"
	}
	return EgressProfile{
		EgressID: rawID, Protocol: st.Endpoint.Protocol, SupportsUDP: st.Endpoint.SupportsUDP,
		Core: "xray", CoreVersion: egress.XrayCoreVersion,
		ExpiresAt: right.until.UTC().Format(time.RFC3339), Config: cfg, Notice: notice,
	}, nil
}
