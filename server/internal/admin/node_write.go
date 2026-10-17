package admin

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 节点登记的写接口（方案 11.5：node-groups / nodes / inbounds）。
//
// 这些接口只改数据库里的登记信息，**从不联系任何机器**：不下发配置、不探测连通性、
// 不断开连接，也不会让一台节点"变得可用"。这一点在响应与文档里都不能含糊——
// 把 status 改成 ONLINE 并不会让隧道通，App 侧仍要求心跳新鲜、证书指纹一致、协议就绪。
//
// 四条边界：
//
//  1. `agent_cert_fingerprint` **不可由这些接口写入**。那是节点注册流程的产物；
//     允许管理员直接填，等于允许把一个节点身份指向任意证书。
//  2. 新建的节点一律 MAINTENANCE，不接受调用方指定状态——它此刻没有证书也没有心跳，
//     任何"立即上线"都是假的。
//  3. 入站的 public_params 走**严格白名单**，并用与节点侧、连接材料侧同一份实现
//     （nodewire）校验。建不出一条会在下发时才被判为「资料无效」的入站——那种入站会
//     让该节点上的**所有**用户一起拿不到材料。
//  4. 所有写操作按 operation_id 幂等，并记录 request_hash：同一 operation_id 换了请求体
//     一律 40901，而不是悄悄返回旧结果让人以为新修改生效了。
type NodeWriteService struct {
	Auth  *Service
	Audit *audit.Service
}

const (
	actionGroupCreate   = "nodes.group_create"
	actionNodeCreate    = "nodes.create"
	actionNodeUpdate    = "nodes.update"
	actionInboundCreate = "nodes.inbound_create"
)

type NodeGroupCreate struct {
	OperationID string `json:"operation_id"`
	Name        string `json:"name"`
	MinTier     *int   `json:"min_tier"`
}

type NodeCreate struct {
	OperationID   string `json:"operation_id"`
	GroupID       string `json:"group_id"`
	Name          string `json:"name"`
	CountryCode   string `json:"country_code"`
	City          string `json:"city"`
	Region        string `json:"region"`
	Host          string `json:"host"`
	ProbePort     *int   `json:"probe_port"`
	CapacityUsers *int   `json:"capacity_users"`
	SortOrder     *int   `json:"sort_order"`
	IsRecommended *bool  `json:"is_recommended"`
}

type NodeUpdate struct {
	OperationID   string  `json:"operation_id"`
	GroupID       *string `json:"group_id"`
	Name          *string `json:"name"`
	CountryCode   *string `json:"country_code"`
	City          *string `json:"city"`
	Region        *string `json:"region"`
	Host          *string `json:"host"`
	ProbePort     *int    `json:"probe_port"`
	CapacityUsers *int    `json:"capacity_users"`
	SortOrder     *int    `json:"sort_order"`
	IsRecommended *bool   `json:"is_recommended"`
	Status        *string `json:"status"`
}

type InboundCreate struct {
	OperationID  string          `json:"operation_id"`
	Protocol     string          `json:"protocol"`
	Port         *int            `json:"port"`
	Priority     *int            `json:"priority"`
	Status       *string         `json:"status"`
	PublicParams json.RawMessage `json:"public_params"`
}

type ManagedInboundDetail struct {
	ID           string          `json:"id"`
	Protocol     string          `json:"protocol"`
	Port         int             `json:"port"`
	Priority     int             `json:"priority"`
	Status       string          `json:"status"`
	PublicParams json.RawMessage `json:"public_params"`
}

type ManagedInboundList struct {
	Items      []ManagedInboundDetail `json:"items"`
	ObservedAt time.Time              `json:"observed_at"`
}

type ManagedNodeGroupList struct {
	Items      []ManagedNodeGroup `json:"items"`
	ObservedAt time.Time          `json:"observed_at"`
}

func badNodeInput(msg string) error { return httpx.NewError(httpx.ErrBadRequest, msg) }

// operationID 与 device_revoke 用同一套要求：必须是规范写法的 UUIDv4。
func validOperationID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == s
}

// authority 要求 nodes.write，且风险等级必须是 1——权限表被改成低风险也不放行。
func (s *NodeWriteService) authority(ctx context.Context, p Principal, permission string, risk int) (int64, error) {
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
		return 0, httpx.NewError(httpx.ErrForbidden, "当前会话或节点管理权限已失效")
	}
	return id, nil
}

// write 是所有节点写操作的共同骨架：写前核验权限 → 同一 operation_id 串行 → 幂等复用
// → 执行 → 记录幂等与审计 → 提交 → 写后再核验一次权限。
//
// 最后那次核验不是多余的：权限或会话在事务执行期间被吊销时，调用方不该拿到一个
// 看起来成功的响应——数据虽然已经落库，但至少不会让操作者以为自己仍然有权。
func (s *NodeWriteService) write(ctx context.Context, p ApprovalProof, action string, operationID string,
	fn func(context.Context, pgx.Tx, int64, time.Time) (int64, any, json.RawMessage, json.RawMessage, error)) (json.RawMessage, error) {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil || s.Audit == nil {
		return nil, httpx.Internal
	}
	if !validOperationID(operationID) {
		return nil, badNodeInput("operation_id 必须是 UUIDv4")
	}
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	actor, err := s.authority(c, p.Principal, "nodes.write", 1)
	if err != nil {
		return nil, err
	}
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer rollbackApproval(tx)
	// 同一幂等键串行：两个并发的同键请求不会各建一份。
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "admin-node-write:"+operationID); err != nil {
		return nil, err
	}
	var priorAction, priorHash string
	var priorResponse json.RawMessage
	err = tx.QueryRow(c, `SELECT action_code,request_hash,response FROM admin_node_mutations WHERE operation_id=$1`,
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

	now := s.Auth.Clock.Now()
	targetID, out, before, after, err := fn(c, tx, actor, now)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(c, `INSERT INTO admin_node_mutations(admin_user_id,operation_id,action_code,request_hash,target_id,response,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, operationID, action, p.RequestHash, targetID, raw, now); err != nil {
		return nil, err
	}
	if err = s.Audit.Append(c, tx, audit.Event{AdminUserID: &actor, ActionCode: action, TargetType: "node",
		TargetID: &targetID, RequestID: p.RequestID, IP: p.IP, UserAgent: p.UserAgent, Result: "SUCCESS",
		BeforeData: before, AfterData: after}); err != nil {
		return nil, err
	}
	if err = tx.Commit(c); err != nil {
		return nil, err
	}
	if _, err = s.authority(c, p.Principal, "nodes.write", 1); err != nil {
		return nil, err
	}
	return raw, nil
}

func uniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && (constraint == "" || pg.ConstraintName == constraint)
}

// ---------- 节点分组 ----------

func (s *NodeWriteService) CreateGroup(ctx context.Context, p ApprovalProof, in NodeGroupCreate) (json.RawMessage, error) {
	if !nodeText(in.Name, 64, false) {
		return nil, badNodeInput("分组名称必须是 1–64 个可见字符")
	}
	if in.MinTier == nil || *in.MinTier < 0 || *in.MinTier > 9 {
		return nil, badNodeInput("min_tier 必须在 0–9 之间")
	}
	return s.write(ctx, p, actionGroupCreate, in.OperationID,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var g ManagedNodeGroup
			err := tx.QueryRow(c, `INSERT INTO node_groups(name,min_tier) VALUES($1,$2)
 RETURNING id::text,name,min_tier`, in.Name, *in.MinTier).Scan(&g.ID, &g.Name, &g.MinTier)
			if uniqueViolation(err, "node_groups_name_key") {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "同名分组已存在")
			}
			if err != nil {
				return 0, nil, nil, nil, err
			}
			id, err := positiveApprovalID(g.ID)
			if err != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			// 审计快照只放白名单允许的字符串字段；完整内容在 admin_node_mutations.response 里。
			after, _ := json.Marshal(map[string]string{"target_id": g.ID, "name": g.Name})
			return id, g, nil, after, nil
		})
}

func (s *NodeReadService) Groups(ctx context.Context, p Principal) (out ManagedNodeGroupList, err error) {
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		rows, e := tx.Query(c, `SELECT id::text,name,min_tier FROM node_groups ORDER BY min_tier,id LIMIT 201`)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedNodeGroupList{Items: []ManagedNodeGroup{}, ObservedAt: now}
		for rows.Next() {
			var g ManagedNodeGroup
			if e = rows.Scan(&g.ID, &g.Name, &g.MinTier); e != nil {
				return e
			}
			if _, e = positiveApprovalID(g.ID); e != nil {
				return httpx.Internal
			}
			// 宁可整体失败，也不返回一份被悄悄截断的列表。
			if !nodeText(g.Name, 64, false) || g.MinTier < 0 || g.MinTier > 9 || len(out.Items) >= 200 {
				return httpx.Internal
			}
			out.Items = append(out.Items, g)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedNodeGroupList{}, err
	}
	return out, nil
}

// ---------- 节点 ----------

func (s *NodeWriteService) CreateNode(ctx context.Context, p ApprovalProof, in NodeCreate) (json.RawMessage, error) {
	groupID, err := positiveApprovalID(in.GroupID)
	if err != nil {
		return nil, badNodeInput("group_id 无效")
	}
	if !nodeText(in.Name, 64, false) || !nodeCountryCode.MatchString(in.CountryCode) ||
		!nodeText(in.City, 64, true) || !nodeText(in.Region, 32, false) || !validNodeHost(in.Host) {
		return nil, badNodeInput("节点资料不合法")
	}
	port, capacity, order := 443, 500, 0
	if err := optionalInt(in.ProbePort, &port, 1, 65535); err != nil {
		return nil, badNodeInput("probe_port 必须在 1–65535 之间")
	}
	if err := optionalInt(in.CapacityUsers, &capacity, 0, 1000000); err != nil {
		return nil, badNodeInput("capacity_users 必须在 0–1000000 之间")
	}
	if err := optionalInt(in.SortOrder, &order, -100000, 100000); err != nil {
		return nil, badNodeInput("sort_order 超出范围")
	}
	recommended := in.IsRecommended != nil && *in.IsRecommended

	return s.write(ctx, p, actionNodeCreate, in.OperationID,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var exists bool
			if e := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM node_groups WHERE id=$1)`, groupID).Scan(&exists); e != nil {
				return 0, nil, nil, nil, e
			}
			if !exists {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "节点分组不存在")
			}
			// status 恒为 MAINTENANCE：新节点没有证书也没有心跳，不能装作已上线。
			var id int64
			if e := tx.QueryRow(c, `INSERT INTO nodes(group_id,name,country_code,city,region,host,probe_port,
 status,capacity_users,is_recommended,sort_order,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,'MAINTENANCE',$8,$9,$10,$11,$11) RETURNING id`,
				groupID, in.Name, in.CountryCode, in.City, in.Region, in.Host, port, capacity, recommended, order, now).
				Scan(&id); e != nil {
				return 0, nil, nil, nil, e
			}
			n, e := scanManagedNode(tx.QueryRow(c, `SELECT `+managedNodeColumns+managedNodeJoin+` WHERE n.id=$1`, id), now)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			after, _ := json.Marshal(map[string]string{"target_id": n.ID, "name": n.Name, "status": n.Status})
			return id, n, nil, after, nil
		})
}

var nodeUpdatableStatus = map[string]bool{"ONLINE": true, "OFFLINE": true, "MAINTENANCE": true, "DISABLED": true}

func (s *NodeWriteService) UpdateNode(ctx context.Context, p ApprovalProof, nodeID int64, in NodeUpdate) (json.RawMessage, error) {
	if nodeID <= 0 {
		return nil, badNodeInput("节点编号无效")
	}
	var groupID int64
	if in.GroupID != nil {
		id, err := positiveApprovalID(*in.GroupID)
		if err != nil {
			return nil, badNodeInput("group_id 无效")
		}
		groupID = id
	}
	if in.Name != nil && !nodeText(*in.Name, 64, false) {
		return nil, badNodeInput("节点名称必须是 1–64 个可见字符")
	}
	if in.CountryCode != nil && !nodeCountryCode.MatchString(*in.CountryCode) {
		return nil, badNodeInput("country_code 必须是两位大写字母")
	}
	if in.City != nil && !nodeText(*in.City, 64, true) {
		return nil, badNodeInput("城市名不合法")
	}
	if in.Region != nil && !nodeText(*in.Region, 32, false) {
		return nil, badNodeInput("区域名不合法")
	}
	if in.Host != nil && !validNodeHost(*in.Host) {
		return nil, badNodeInput("host 不合法")
	}
	if in.Status != nil && !nodeUpdatableStatus[*in.Status] {
		return nil, badNodeInput("status 不合法")
	}
	var port, capacity, order int
	if err := optionalInt(in.ProbePort, &port, 1, 65535); err != nil {
		return nil, badNodeInput("probe_port 必须在 1–65535 之间")
	}
	if err := optionalInt(in.CapacityUsers, &capacity, 0, 1000000); err != nil {
		return nil, badNodeInput("capacity_users 必须在 0–1000000 之间")
	}
	if err := optionalInt(in.SortOrder, &order, -100000, 100000); err != nil {
		return nil, badNodeInput("sort_order 超出范围")
	}
	if in.GroupID == nil && in.Name == nil && in.CountryCode == nil && in.City == nil && in.Region == nil &&
		in.Host == nil && in.ProbePort == nil && in.CapacityUsers == nil && in.SortOrder == nil &&
		in.IsRecommended == nil && in.Status == nil {
		return nil, badNodeInput("至少要提供一个待修改的字段")
	}

	return s.write(ctx, p, actionNodeUpdate, in.OperationID,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			// 先锁住这一行：并发的两次修改不会互相覆盖到一个谁也没提交过的组合。
			var locked int64
			e := tx.QueryRow(c, `SELECT id FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&locked)
			if errors.Is(e, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "节点不存在")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			prev, e := scanManagedNode(tx.QueryRow(c, `SELECT `+managedNodeColumns+managedNodeJoin+` WHERE n.id=$1`, nodeID), now)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			if in.GroupID != nil {
				var exists bool
				if e = tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM node_groups WHERE id=$1)`, groupID).Scan(&exists); e != nil {
					return 0, nil, nil, nil, e
				}
				if !exists {
					return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "节点分组不存在")
				}
			}
			// COALESCE 形式：只有显式给出的字段才会变，没给的保持原值。
			// agent_cert_fingerprint、last_heartbeat_at、online_count、load_score 一概不在这里。
			if _, e = tx.Exec(c, `UPDATE nodes SET
 group_id=COALESCE($2,group_id), name=COALESCE($3,name), country_code=COALESCE($4,country_code),
 city=COALESCE($5,city), region=COALESCE($6,region), host=COALESCE($7,host),
 probe_port=COALESCE($8,probe_port), capacity_users=COALESCE($9,capacity_users),
 sort_order=COALESCE($10,sort_order), is_recommended=COALESCE($11,is_recommended),
 status=COALESCE($12,status), updated_at=$13 WHERE id=$1`,
				nodeID, nullableInt64(in.GroupID, groupID), in.Name, in.CountryCode, in.City, in.Region, in.Host,
				nullableInt(in.ProbePort), nullableInt(in.CapacityUsers), nullableInt(in.SortOrder),
				in.IsRecommended, in.Status, now); e != nil {
				return 0, nil, nil, nil, e
			}
			n, e := scanManagedNode(tx.QueryRow(c, `SELECT `+managedNodeColumns+managedNodeJoin+` WHERE n.id=$1`, nodeID), now)
			if e != nil {
				return 0, nil, nil, nil, e
			}
			before, _ := json.Marshal(map[string]string{"target_id": prev.ID, "name": prev.Name, "status": prev.Status})
			after, _ := json.Marshal(map[string]string{"target_id": n.ID, "name": n.Name, "status": n.Status})
			return nodeID, n, before, after, nil
		})
}

// ---------- 入站 ----------

func (s *NodeWriteService) CreateInbound(ctx context.Context, p ApprovalProof, nodeID int64, in InboundCreate) (json.RawMessage, error) {
	if nodeID <= 0 {
		return nil, badNodeInput("节点编号无效")
	}
	// 本轮只支持 WIREGUARD。其余协议宁可明说不支持，也不把没校验过的参数存进库——
	// 一条无效入站会让该节点上的所有用户一起拿不到连接材料。
	if in.Protocol != "WIREGUARD" {
		return nil, badNodeInput("当前只支持 WIREGUARD 入站")
	}
	if in.Port == nil || *in.Port < 1 || *in.Port > 65535 {
		return nil, badNodeInput("port 必须在 1–65535 之间")
	}
	priority := 100
	if err := optionalInt(in.Priority, &priority, 0, 10000); err != nil {
		return nil, badNodeInput("priority 必须在 0–10000 之间")
	}
	// 默认 DISABLED：新建入站不该在管理员还没核对配置时就立刻对用户生效。
	status := "DISABLED"
	if in.Status != nil {
		if *in.Status != "ENABLED" && *in.Status != "DISABLED" {
			return nil, badNodeInput("status 必须是 ENABLED 或 DISABLED")
		}
		status = *in.Status
	}
	params, err := validateWireGuardParams(in.PublicParams)
	if err != nil {
		return nil, err
	}

	return s.write(ctx, p, actionInboundCreate, in.OperationID,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var exists bool
			if e := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1)`, nodeID).Scan(&exists); e != nil {
				return 0, nil, nil, nil, e
			}
			if !exists {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "节点不存在")
			}
			var v ManagedInboundDetail
			e := tx.QueryRow(c, `INSERT INTO node_inbounds(node_id,protocol,port,public_params,priority,status,created_at)
 VALUES($1,$2,$3,$4::jsonb,$5,$6,$7) RETURNING id::text,protocol,port,priority,status,public_params`,
				nodeID, in.Protocol, *in.Port, string(params), priority, status, now).
				Scan(&v.ID, &v.Protocol, &v.Port, &v.Priority, &v.Status, &v.PublicParams)
			if uniqueViolation(e, "node_inbounds_node_id_protocol_port_key") {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "该节点上同协议同端口的入站已存在")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			id, e := positiveApprovalID(v.ID)
			if e != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			after, _ := json.Marshal(map[string]string{"target_id": v.ID, "code": v.Protocol, "status": v.Status})
			return id, v, nil, after, nil
		})
}

func (s *NodeReadService) Inbounds(ctx context.Context, p Principal, nodeID int64) (out ManagedInboundList, err error) {
	if nodeID <= 0 {
		return out, badNodeInput("节点编号无效")
	}
	err = s.read(ctx, p, func(c context.Context, tx pgx.Tx, now time.Time) error {
		var exists bool
		if e := tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1)`, nodeID).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return httpx.NewError(httpx.ErrNotFound, "节点不存在")
		}
		rows, e := tx.Query(c, `SELECT id::text,protocol,port,priority,status,public_params
 FROM node_inbounds WHERE node_id=$1 ORDER BY priority,id LIMIT 101`, nodeID)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedInboundList{Items: []ManagedInboundDetail{}, ObservedAt: now}
		for rows.Next() {
			var v ManagedInboundDetail
			if e = rows.Scan(&v.ID, &v.Protocol, &v.Port, &v.Priority, &v.Status, &v.PublicParams); e != nil {
				return e
			}
			if _, e = positiveApprovalID(v.ID); e != nil {
				return httpx.Internal
			}
			if v.Port < 1 || v.Port > 65535 || (v.Status != "ENABLED" && v.Status != "DISABLED") || len(out.Items) >= 100 {
				return httpx.Internal
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedInboundList{}, err
	}
	return out, nil
}

// validateWireGuardParams 按严格白名单校验入站公开参数，并用与节点侧、连接材料侧
// **同一份**实现（nodewire）校验公钥与地址。返回规范化后的 JSON——库里存的就是
// 校验过的那份，不是调用方原样的字节。
func validateWireGuardParams(raw json.RawMessage) (json.RawMessage, error) {
	bad := badNodeInput("public_params 不合法：只接受 interface、public_key、config_version、dns、mtu 五个字段")
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, bad
	}
	var p struct {
		Interface     string   `json:"interface"`
		PublicKey     string   `json:"public_key"`
		ConfigVersion string   `json:"config_version"`
		DNS           []string `json:"dns,omitempty"`
		MTU           int      `json:"mtu,omitempty"`
	}
	// StrictJSON 拒绝未知字段、重复键与 null——白名单之外的任何东西都进不来，
	// 因此这里结构上不可能存进私钥。
	if nodewire.StrictJSON(raw, &p) != nil {
		return nil, bad
	}
	if !nodewire.ValidWGInterface(p.Interface) {
		return nil, badNodeInput("interface 必须是 1–15 个字母、数字、点、下划线或短横线")
	}
	if !nodewire.ValidWGPublicKey(p.PublicKey) {
		return nil, badNodeInput("public_key 必须是 Base64 编码的 32 字节公钥")
	}
	if n, e := strconv.ParseUint(p.ConfigVersion, 10, 63); e != nil || n == 0 ||
		strconv.FormatUint(n, 10) != p.ConfigVersion {
		return nil, badNodeInput("config_version 必须是不含前导零的正整数字符串")
	}
	if len(p.DNS) > 4 {
		return nil, badNodeInput("dns 最多 4 条")
	}
	for _, d := range p.DNS {
		if !nodewire.ValidWGPeerIP(d + "/32") {
			return nil, badNodeInput("dns 必须是私有网段的 IPv4 地址")
		}
	}
	if p.MTU != 0 && (p.MTU < 1280 || p.MTU > 1500) {
		return nil, badNodeInput("mtu 必须在 1280–1500 之间")
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, httpx.Internal
	}
	return out, nil
}

func optionalInt(in *int, dst *int, min, max int) error {
	if in == nil {
		return nil
	}
	if *in < min || *in > max {
		return errors.New("out of range")
	}
	*dst = *in
	return nil
}

func nullableInt(v *int) *int { return v }

// validNodeHost 只登记，不做 DNS 解析或连通性探测：主机名或 IP 的字面合法性即可。
func validNodeHost(s string) bool {
	if len(s) == 0 || len(s) > 255 || !nodeText(s, 255, false) {
		return false
	}
	return nodeHostPattern.MatchString(s)
}

var nodeHostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:_-]{0,253}[A-Za-z0-9])?$`)

func nullableInt64(present *string, value int64) *int64 {
	if present == nil {
		return nil
	}
	return &value
}
