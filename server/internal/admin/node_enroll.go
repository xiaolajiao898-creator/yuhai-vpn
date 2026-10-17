package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuhai-vpn/server/internal/httpx"
)

// 节点身份的两个 L2 操作：签发一次性注册令牌、吊销证书绑定。
//
// 与 nodes.write（改登记信息）分开成两个独立能力 nodes.enroll_token / nodes.revoke，
// 理由很直白：给节点发身份等于决定「哪台机器可以代表这个节点」，那是比改名字改城市高一档的事；
// 而「能发身份」和「能作废身份」又可以分给不同的人。两者都要求二次验证。
type NodeEnrollService struct{ Security *ApprovalService }

const (
	actionEnrollToken = "nodes.enroll_token"
	actionNodeRevoke  = "nodes.revoke"
	enrollTokenTTL    = 24 * time.Hour
)

type NodeSecurityInput struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
}

type EnrollTokenResult struct {
	OperationID string    `json:"operation_id"`
	NodeID      string    `json:"node_id"`
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (in NodeSecurityInput) valid() bool {
	return validOperationID(in.OperationID) && approvalText(in.Reason)
}

// newEnrollToken 生成 32 字节随机令牌。用 URL 安全无填充编码，方便运维复制粘贴进
// 节点的配置文件而不被 shell 或 URL 转义搅乱。
func newEnrollToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// secure 是两个 L2 操作的共同骨架：二次验证 → 锁权限 → 幂等检查 → 执行 → 审计。
//
// 幂等记录里**不存响应**：注册令牌的明文只存在于首次响应中，存下来就等于把密钥
// 留在库里可回放。所以重复提交按 40901 拒绝，而不是重放。
func (s *NodeEnrollService) secure(ctx context.Context, p ApprovalProof, action string, nodeID int64,
	in NodeSecurityInput, replay bool,
	fn func(context.Context, pgx.Tx, int64, time.Time) (json.RawMessage, error)) (json.RawMessage, error) {
	if s == nil || s.Security == nil || s.Security.Auth == nil || s.Security.Auth.Pool == nil ||
		s.Security.Auth.Clock == nil || s.Security.Audit == nil {
		return nil, httpx.Internal
	}
	a := s.Security
	event := a.auditEvent(p, action, "node", nodeID)
	event.UserAgent = ""
	var err error
	defer func() {
		if err != nil {
			err = a.RecordFailure(ctx, event, err)
		}
	}()
	if nodeID <= 0 || !in.valid() {
		err = httpx.NewError(httpx.ErrBadRequest, "节点安全操作资料无效")
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
		"admin-node-security:"+in.OperationID); e != nil {
		err = e
		return nil, err
	}
	var priorAction, priorHash string
	var priorNode int64
	e = tx.QueryRow(c, `SELECT action_code,request_hash,node_id FROM admin_node_security_mutations
 WHERE operation_id=$1`, in.OperationID).Scan(&priorAction, &priorHash, &priorNode)
	switch {
	case e == nil:
		if priorAction != action || priorHash != p.RequestHash || priorNode != nodeID {
			err = httpx.NewError(httpx.ErrConflict, "该 operation_id 已用于不同的请求")
			return nil, err
		}
		if !replay {
			// 令牌只显示一次，无法重放；明说，让调用方换一个 operation_id 重新签发。
			err = httpx.NewError(httpx.ErrConflict, "该 operation_id 已签发过令牌；令牌只显示一次，请用新的 operation_id 重新签发")
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
		return nil, nil
	case !errors.Is(e, pgx.ErrNoRows):
		err = e
		return nil, err
	}

	if e = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, action, p.RequestHash, p.IP); e != nil {
		err = e
		return nil, err
	}
	event.StepUpVerified = true

	var locked int64
	e = tx.QueryRow(c, `SELECT id FROM nodes WHERE id=$1 FOR UPDATE`, nodeID).Scan(&locked)
	if errors.Is(e, pgx.ErrNoRows) {
		err = httpx.NewError(httpx.ErrNotFound, "节点不存在")
		return nil, err
	}
	if e != nil {
		err = e
		return nil, err
	}
	now := a.Auth.Clock.Now()
	out, e := fn(c, tx, actor.id, now)
	if e != nil {
		err = e
		return nil, err
	}
	if _, e = tx.Exec(c, `INSERT INTO admin_node_security_mutations(admin_user_id,operation_id,action_code,request_hash,node_id,session_id,proof_hash,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, actor.id, in.OperationID, action, p.RequestHash, nodeID,
		p.Principal.SessionID, tokenHash(p.Token), now); e != nil {
		err = e
		return nil, err
	}
	event.AfterData, _ = json.Marshal(map[string]string{
		"target_id": strconv.FormatInt(nodeID, 10), "decision_note": in.Reason})
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
	return out, nil
}

// IssueEnrollToken 为节点签发一次性注册令牌。库里只存 SHA-256，明文只在返回值里出现一次。
func (s *NodeEnrollService) IssueEnrollToken(ctx context.Context, p ApprovalProof, nodeID int64,
	in NodeSecurityInput) (out EnrollTokenResult, err error) {
	var token string
	raw, err := s.secure(ctx, p, actionEnrollToken, nodeID, in, false,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (json.RawMessage, error) {
			// 同一节点任何时刻最多只有一把有效令牌：先把此前未使用的置为立即过期。
			// 不改 used_at——那会谎称它被用过。
			if _, e := tx.Exec(c, `UPDATE node_enroll_tokens SET expires_at=$2
 WHERE node_id=$1 AND used_at IS NULL AND expires_at>$2`, nodeID, now); e != nil {
				return nil, e
			}
			plain, e := newEnrollToken()
			if e != nil {
				return nil, e
			}
			expires := now.Add(enrollTokenTTL)
			if _, e = tx.Exec(c, `INSERT INTO node_enroll_tokens(node_id,token_hash,expires_at,created_by,created_at)
 VALUES($1,$2,$3,$4,$5)`, nodeID, tokenHash(plain), expires, actor, now); e != nil {
				return nil, e
			}
			token = plain
			result := EnrollTokenResult{OperationID: in.OperationID, NodeID: strconv.FormatInt(nodeID, 10),
				Token: plain, ExpiresAt: expires}
			return json.Marshal(result)
		})
	if err != nil {
		return EnrollTokenResult{}, err
	}
	if err = json.Unmarshal(raw, &out); err != nil || out.Token != token || out.Token == "" {
		return EnrollTokenResult{}, httpx.Internal
	}
	return out, nil
}

// RevokeNode 解除证书绑定并下线。**不联系那台机器，不断开已建立的隧道**——
// 效果是它下一次连控制面时无法通过身份校验，并从所有用户的节点列表中消失。
func (s *NodeEnrollService) RevokeNode(ctx context.Context, p ApprovalProof, nodeID int64,
	in NodeSecurityInput) error {
	_, err := s.secure(ctx, p, actionNodeRevoke, nodeID, in, true,
		func(c context.Context, tx pgx.Tx, actor int64, now time.Time) (json.RawMessage, error) {
			if _, e := tx.Exec(c, `UPDATE nodes SET agent_cert_fingerprint=NULL,
 agent_cert_prev_fingerprint=NULL,agent_cert_prev_until=NULL,status='DISABLED',updated_at=$2
 WHERE id=$1`, nodeID, now); e != nil {
				return nil, e
			}
			// 未使用的注册令牌一并作废，否则吊销之后还能用旧令牌换一张新证书回来。
			if _, e := tx.Exec(c, `UPDATE node_enroll_tokens SET expires_at=$2
 WHERE node_id=$1 AND used_at IS NULL AND expires_at>$2`, nodeID, now); e != nil {
				return nil, e
			}
			return nil, nil
		})
	return err
}
