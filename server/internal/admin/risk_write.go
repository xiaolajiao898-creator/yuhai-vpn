package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 风控处置：黑名单增删查、账户状态变更（方案 11.5）。
//
// **关于黑名单，有一件必须说在前面的事：它当前只在邀请链路生效。**
// 命中黑名单会拦下邀请绑定与邀请奖励，但**不会**阻止登录、下单或连接。
// 全项目真正读取 blacklist 的只有 internal/invite/bind.go 与 risk_rewards.go 两处。
// 因此这套接口的定位是「记录并影响邀请风控」，不是「把某人挡在门外」——
// 契约描述里也是这么写的，免得运维加了一条却发现人照样在用。
//
// 账户状态则是真的有效果，但三个状态**效果不同**：
//   - BANNED：鉴权中间件直接挡掉所有已认证接口，等于完全锁死。
//   - SUSPENDED：仍可登录查看账户，但拿不到连接材料（40305），且从各节点 peer 清单中消失。
//   - ACTIVE：恢复。
//
// 三者都**不会立即切断已经建立的隧道**，效果体现在「下一次取材料 / 下一次节点同步」。
type RiskWriteService struct{ Security *ApprovalService }

const (
	actionBlacklistAdd    = "risk.blacklist_add"
	actionBlacklistRemove = "risk.blacklist_remove"
	actionUserStatus      = "users.status"
)

type ManagedBlacklistEntry struct {
	ID          string     `json:"id"`
	TargetType  string     `json:"target_type"`
	TargetValue string     `json:"target_value"`
	Effects     []string   `json:"effects"`
	Reason      string     `json:"reason"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RemovedAt   *time.Time `json:"removed_at"`
}

type ManagedBlacklistPage struct {
	Items      []ManagedBlacklistEntry `json:"items"`
	Total      string                  `json:"total"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	ObservedAt time.Time               `json:"observed_at"`
}

type BlacklistAddInput struct {
	OperationID string     `json:"operation_id"`
	TargetType  string     `json:"target_type"`
	TargetValue string     `json:"target_value"`
	Effects     []string   `json:"effects"`
	Reason      string     `json:"reason"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type RiskReasonInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

type UserStatusInput struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
}

var blacklistTargets = map[string]bool{"USER": true, "DEVICE": true, "FINGERPRINT": true,
	"PAY_ADDRESS": true, "IP_PREFIX": true, "INVITE_CODE": true}

// 只接受代码里真正会检查的三个 effect。写入一个没有任何地方会读的值，
// 等于给运维一个假的安全感——宁可当场拒绝。
var blacklistEffects = map[string]bool{"INVITE_BIND": true, "INVITE_REWARD": true, "ALL": true}

var userStatuses = map[string]bool{"ACTIVE": true, "SUSPENDED": true, "BANNED": true}

// 幂等表名会被拼进 SQL。它只能是本包写死的这几张表之一——列成白名单，
// 是为了让「这里拼了一个变量进 SQL」这件事一眼可见，而不是埋在字符串里。
var mutationTables = map[string]bool{
	"admin_risk_mutations":    true,
	"admin_invite_mutations":  true,
	"admin_plan_mutations":    true,
	"admin_config_mutations":  true,
	"admin_finance_mutations": true,
	"admin_export_mutations":  true,
}

// securedMutation 是后台 L2 写操作的公共骨架：二次验证 → 锁权限 → 幂等 → 执行 → 审计。
//
// 与 NodeEnrollService.secure 仍然分开：节点那条路的锁定对象是节点行本身，重放语义也不同
// （签发令牌只存哈希，天然不可重放）。这里合并的是形状确实相同的两处——风控处置与邀请复核，
// 它们除了幂等表名与锁前缀之外逐行一致。第三份复制就该停手了。
func securedMutation(ctx context.Context, a *ApprovalService, table, lockPrefix string,
	p ApprovalProof, action, targetType string, targetID int64, operationID, reason string,
	fn func(context.Context, pgx.Tx, int64, time.Time) (int64, any, json.RawMessage, json.RawMessage, error),
) (json.RawMessage, error) {
	if a == nil || a.Auth == nil || a.Auth.Pool == nil || a.Auth.Clock == nil || a.Audit == nil ||
		!mutationTables[table] {
		return nil, httpx.Internal
	}
	event := a.auditEvent(p, action, targetType, targetID)
	event.UserAgent = ""
	var err error
	defer func() {
		if err != nil {
			err = a.RecordFailure(ctx, event, err)
		}
	}()
	if !validOperationID(operationID) || !approvalText(reason) {
		err = httpx.NewError(httpx.ErrBadRequest, "风控操作资料无效")
		return nil, err
	}
	if p.Token == "" {
		err = httpx.NewError(httpx.ErrStepUpRequired, "请先完成二次验证")
		return nil, err
	}
	if !validToken(p.Token) || !stepUpHash.MatchString(p.RequestHash) {
		err = errStepUp
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, e := a.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		err = e
		return nil, err
	}
	defer rollbackApproval(tx)
	actor, e := a.Auth.lockStepUpAuthority(c, tx, p.Principal, action, p.IP)
	if e != nil {
		err = e
		return nil, err
	}
	if e = lockApprovalPermission(c, tx, actor.id, action, 2, a.Auth.Clock.Now()); e != nil {
		err = e
		return nil, err
	}

	// 同一 operation_id 串行：用事务级顾问锁而不是对幂等表加 FOR UPDATE——
	// SELECT ... FOR UPDATE 需要该表的 UPDATE 权限，而这张表只会被插入一次、
	// 从不更新。为了拿锁去多要一个写权限，是不必要的权限扩大。
	if _, e = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		lockPrefix+":"+operationID); e != nil {
		err = e
		return nil, err
	}
	var priorAction, priorHash string
	var priorResponse json.RawMessage
	e = tx.QueryRow(c, `SELECT action_code,request_hash,response FROM `+table+
		` WHERE operation_id=$1`, operationID).Scan(&priorAction, &priorHash, &priorResponse)
	switch {
	case e == nil:
		if priorAction != action || priorHash != p.RequestHash {
			err = httpx.NewError(httpx.ErrConflict, "该 operation_id 已用于不同的请求")
			return nil, err
		}
		if e = a.freshProof(c, tx, p); e != nil {
			err = e
			return nil, err
		}
		if e = tx.Commit(c); e != nil {
			err = e
			return nil, err
		}
		return priorResponse, nil
	case !errors.Is(e, pgx.ErrNoRows):
		err = e
		return nil, err
	}

	if e = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, action, p.RequestHash, p.IP); e != nil {
		err = e
		return nil, err
	}
	event.StepUpVerified = true

	now := a.Auth.Clock.Now()
	written, out, before, after, e := fn(c, tx, actor.id, now)
	if e != nil {
		err = e
		return nil, err
	}
	raw, e := json.Marshal(out)
	if e != nil {
		err = e
		return nil, err
	}
	if _, e = tx.Exec(c, `INSERT INTO `+table+`(admin_user_id,operation_id,action_code,
 request_hash,target_id,session_id,proof_hash,response,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		actor.id, operationID, action, p.RequestHash, written, p.Principal.SessionID,
		tokenHash(p.Token), raw, now); e != nil {
		err = e
		return nil, err
	}
	event.TargetID = &written
	event.BeforeData, event.AfterData = before, after
	if e = a.Audit.Append(c, tx, event); e != nil {
		err = e
		return nil, err
	}
	if e = a.freshProof(c, tx, p); e != nil {
		err = e
		return nil, err
	}
	if e = tx.Commit(c); e != nil {
		err = e
		return nil, err
	}
	return raw, nil
}

func (s *RiskWriteService) securityWrite(ctx context.Context, p ApprovalProof, action, targetType string,
	targetID int64, operationID, reason string,
	fn func(context.Context, pgx.Tx, int64, time.Time) (int64, any, json.RawMessage, json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, httpx.Internal
	}
	return securedMutation(ctx, s.Security, "admin_risk_mutations", "admin-risk-write",
		p, action, targetType, targetID, operationID, reason, fn)
}

// AddBlacklist 新增一条黑名单。同一目标同时只允许一条生效（由部分唯一索引保证）。
func (s *RiskWriteService) AddBlacklist(ctx context.Context, p ApprovalProof, in BlacklistAddInput) (out ManagedBlacklistEntry, err error) {
	if !blacklistTargets[in.TargetType] {
		return out, httpx.NewError(httpx.ErrBadRequest, "目标类型无效")
	}
	if !nodeText(in.TargetValue, 128, false) {
		return out, httpx.NewError(httpx.ErrBadRequest, "目标值必须是 1–128 个可见字符")
	}
	if len(in.Effects) == 0 || len(in.Effects) > 3 {
		return out, httpx.NewError(httpx.ErrBadRequest, "effects 必须是 1–3 项")
	}
	seen := map[string]bool{}
	for _, e := range in.Effects {
		if !blacklistEffects[e] || seen[e] {
			return out, httpx.NewError(httpx.ErrBadRequest, "effects 只接受 INVITE_BIND、INVITE_REWARD、ALL 且不得重复")
		}
		seen[e] = true
	}
	// 已经过期的截止时间等于一条从未生效的记录，直接拒绝而不是默默写进去。
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.Security.Auth.Clock.Now()) {
		return out, httpx.NewError(httpx.ErrBadRequest, "expires_at 必须晚于当前时间")
	}

	raw, err := s.securityWrite(ctx, p, actionBlacklistAdd, "blacklist", 0, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var entry ManagedBlacklistEntry
			e := tx.QueryRow(c, `INSERT INTO blacklist(target_type,target_value,effects,reason,status,
 expires_at,created_by,created_at) VALUES($1,$2,$3,$4,'ACTIVE',$5,$6,$7)
 RETURNING id::text,target_type,target_value,effects,reason,status,expires_at,created_at,removed_at`,
				in.TargetType, in.TargetValue, in.Effects, in.Reason, in.ExpiresAt, actor, now).
				Scan(&entry.ID, &entry.TargetType, &entry.TargetValue, &entry.Effects, &entry.Reason,
					&entry.Status, &entry.ExpiresAt, &entry.CreatedAt, &entry.RemovedAt)
			var pg *pgconn.PgError
			if errors.As(e, &pg) && pg.Code == "23505" {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrConflict, "该目标已有生效的黑名单条目")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			id, e := positiveApprovalID(entry.ID)
			if e != nil {
				return 0, nil, nil, nil, httpx.Internal
			}
			after, _ := json.Marshal(map[string]string{"target_id": entry.ID, "target_type": entry.TargetType,
				"status": entry.Status, "decision_note": in.Reason})
			return id, entry, nil, after, nil
		})
	if err != nil {
		return ManagedBlacklistEntry{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return ManagedBlacklistEntry{}, httpx.Internal
	}
	return out, nil
}

// RemoveBlacklist 解除一条黑名单：置为 REMOVED 并留痕，**不删除记录**。
func (s *RiskWriteService) RemoveBlacklist(ctx context.Context, p ApprovalProof, entryID int64, in RiskReasonInput) error {
	if entryID <= 0 {
		return httpx.NewError(httpx.ErrBadRequest, "条目编号无效")
	}
	_, err := s.securityWrite(ctx, p, actionBlacklistRemove, "blacklist", entryID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var status string
			e := tx.QueryRow(c, `SELECT status FROM blacklist WHERE id=$1 FOR UPDATE`, entryID).Scan(&status)
			if errors.Is(e, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "黑名单条目不存在")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			// 已解除的再次解除是幂等的，不改写原来的解除人与时间。
			if status == "ACTIVE" {
				if _, e = tx.Exec(c, `UPDATE blacklist SET status='REMOVED',removed_by=$2,removed_at=$3
 WHERE id=$1 AND status='ACTIVE'`, entryID, actor, now); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			before, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(entryID, 10), "status": status})
			after, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(entryID, 10),
				"status": "REMOVED", "decision_note": in.Reason})
			return entryID, struct{}{}, before, after, nil
		})
	return err
}

// SetUserStatus 停用 / 封禁 / 恢复账户。不吊销登录会话——见文件开头的说明。
func (s *RiskWriteService) SetUserStatus(ctx context.Context, p ApprovalProof, userID int64, in UserStatusInput) error {
	if userID <= 0 {
		return httpx.NewError(httpx.ErrBadRequest, "用户编号无效")
	}
	if !userStatuses[in.Status] {
		return httpx.NewError(httpx.ErrBadRequest, "账户状态无效")
	}
	_, err := s.securityWrite(ctx, p, actionUserStatus, "user", userID, in.OperationID, in.Reason,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (int64, any, json.RawMessage, json.RawMessage, error) {
			var prev string
			e := tx.QueryRow(c, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&prev)
			if errors.Is(e, pgx.ErrNoRows) {
				return 0, nil, nil, nil, httpx.NewError(httpx.ErrNotFound, "用户不存在")
			}
			if e != nil {
				return 0, nil, nil, nil, e
			}
			if prev != in.Status {
				if _, e = tx.Exec(c, `UPDATE users SET status=$2,updated_at=$3 WHERE id=$1`,
					userID, in.Status, now); e != nil {
					return 0, nil, nil, nil, e
				}
			}
			before, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(userID, 10), "status": prev})
			after, _ := json.Marshal(map[string]string{"target_id": strconv.FormatInt(userID, 10),
				"status": in.Status, "decision_note": in.Reason})
			return userID, struct{}{}, before, after, nil
		})
	return err
}

// ---------- 只读 ----------

type RiskReadService struct{ Auth *Service }

func (s *RiskReadService) Blacklist(ctx context.Context, p Principal, f FinanceFilter, targetType string) (out ManagedBlacklistPage, err error) {
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Status != "" && f.Status != "ACTIVE" && f.Status != "REMOVED" {
		return out, httpx.NewError(httpx.ErrBadRequest, "状态无效")
	}
	if targetType != "" && !blacklistTargets[targetType] {
		return out, httpx.NewError(httpx.ErrBadRequest, "目标类型无效")
	}
	read := &FinanceReadService{Auth: s.Auth}
	err = read.readWith(ctx, p, "risk.read", func(c context.Context, tx pgx.Tx, now time.Time) error {
		const where = ` FROM blacklist b WHERE ($1='' OR b.status=$1) AND ($2='' OR b.target_type=$2)`
		var total int64
		if e := tx.QueryRow(c, `SELECT count(*)`+where, f.Status, targetType).Scan(&total); e != nil {
			return e
		}
		rows, e := tx.Query(c, `SELECT b.id::text,b.target_type,b.target_value,b.effects,b.reason,b.status,
 b.expires_at,b.created_at,b.removed_at`+where+` ORDER BY b.created_at DESC,b.id DESC LIMIT $3 OFFSET $4`,
			f.Status, targetType, f.PageSize, (f.Page-1)*f.PageSize)
		if e != nil {
			return e
		}
		defer rows.Close()
		out = ManagedBlacklistPage{Items: []ManagedBlacklistEntry{}, Total: strconv.FormatInt(total, 10),
			Page: f.Page, PageSize: f.PageSize, ObservedAt: now}
		for rows.Next() {
			var v ManagedBlacklistEntry
			if e = rows.Scan(&v.ID, &v.TargetType, &v.TargetValue, &v.Effects, &v.Reason, &v.Status,
				&v.ExpiresAt, &v.CreatedAt, &v.RemovedAt); e != nil {
				return e
			}
			if _, e = positiveApprovalID(v.ID); e != nil {
				return httpx.Internal
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return ManagedBlacklistPage{}, err
	}
	return out, nil
}
