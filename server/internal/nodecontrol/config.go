package nodecontrol

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 节点自取配置（方案 node.yaml 的 GET /node/v1/config）。
//
// 这个接口只回答一个问题：**这台节点应该监听什么**。它不包含任何用户凭证，
// 也不包含任何密钥材料——入站的 public_params 在后台写入时就过了严格白名单，
// 结构上放不进私钥（见 internal/admin/node_write.go）。
//
// 只返回 ENABLED 的入站。管理员把一条入站置为 DISABLED，节点下次取配置时它就消失了；
// 反过来也一样。这里不做任何"顺便帮你打开"的事。
type NodeInbound struct {
	ID       string          `json:"id"`
	Protocol string          `json:"protocol"`
	Port     int             `json:"port"`
	Extra    json.RawMessage `json:"extra"`
}

type AgentParams struct {
	EventPollSeconds      int `json:"event_poll_seconds"`
	HeartbeatSeconds      int `json:"heartbeat_seconds"`
	TrafficReportSeconds  int `json:"traffic_report_seconds"`
	SnapshotVerifyMinutes int `json:"snapshot_verify_minutes"`
}

type NodeConfig struct {
	ConfigVersion int64         `json:"config_version"`
	Inbounds      []NodeInbound `json:"inbounds"`
	Agent         AgentParams   `json:"agent"`
}

// 轮询节奏本轮是全局固定值，不按节点配置——与其给一个谁也没调过的旋钮，
// 不如先让所有节点行为一致，需要分节点调整时再加。
var defaultAgentParams = AgentParams{EventPollSeconds: 10, HeartbeatSeconds: 30,
	TrafficReportSeconds: 60, SnapshotVerifyMinutes: 30}

// configVersion 是配置内容的指纹，**不是递增计数器**。
//
// 这样选是因为节点真正需要的是「和我手上这份一样吗」，而不是「第几版」：
// 内容不变则版本不变（改了又改回去也一样），内容一变必定改变。
// 取 SHA-256 前 7 字节保证落在正整数范围内，且不会是 0。
func configVersion(inbounds []NodeInbound) int64 {
	canonical, err := json.Marshal(inbounds)
	if err != nil {
		return 1
	}
	sum := sha256.Sum256(canonical)
	var padded [8]byte
	copy(padded[1:], sum[:7])
	v := int64(binary.BigEndian.Uint64(padded[:]))
	if v <= 0 {
		return 1
	}
	return v
}

// Config 返回本节点当前应当监听的入站。身份完全来自 mTLS。
func (s *Service) Config(ctx context.Context, state *tls.ConnectionState) (NodeConfig, error) {
	out := NodeConfig{Inbounds: []NodeInbound{}, Agent: defaultAgentParams}
	now := s.Clock.Now()
	id, err := certificateIdentity(state, s.Roots, now)
	if err != nil {
		return out, err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var status string
	var cert nodeCert
	err = s.Pool.QueryRow(c, `SELECT status,`+nodeCertColumns+` FROM nodes WHERE id=$1`, id.id).
		Scan(&status, &cert.Current, &cert.Prev, &cert.Until)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, forbidden()
	}
	if err != nil {
		return out, err
	}
	// 与心跳同一口径：证书必须仍然是这台节点的，且节点没有被停用。
	if status == "DISABLED" || !cert.accepts(id.fingerprint, now) {
		return out, forbidden()
	}

	rows, err := s.Pool.Query(c, `SELECT id::text,protocol,port,public_params FROM node_inbounds
 WHERE node_id=$1 AND status='ENABLED' ORDER BY priority,id LIMIT 101`, id.id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v NodeInbound
		if err = rows.Scan(&v.ID, &v.Protocol, &v.Port, &v.Extra); err != nil {
			return out, err
		}
		// 宁可整体失败，也不给一份被悄悄截断的配置——节点照着半份配置去监听，
		// 表现会是「一部分用户连不上」，比直接报错难查得多。
		if len(out.Inbounds) >= 100 {
			return NodeConfig{}, httpx.NewError(httpx.ErrNodeUnavailable, "入站数量超出上限")
		}
		if v.Port < 1 || v.Port > 65535 || len(v.Extra) == 0 {
			return NodeConfig{}, httpx.NewError(httpx.ErrNodeUnavailable, "入站资料不完整")
		}
		out.Inbounds = append(out.Inbounds, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	out.ConfigVersion = configVersion(out.Inbounds)
	return out, nil
}
