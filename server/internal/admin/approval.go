package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const publishAction = "app_versions.publish"
const approvalDecision = "approvals.decide"
const approvalTTL = 24 * time.Hour

type Approval struct {
	ID                string             `json:"id"`
	ActionCode        string             `json:"action_code"`
	TargetType        string             `json:"target_type"`
	TargetID          *string            `json:"target_id"`
	Status            string             `json:"status"`
	Reason            string             `json:"reason"`
	RequestedBy       string             `json:"requested_by"`
	DecidedBy         *string            `json:"decided_by"`
	CreatedAt         time.Time          `json:"created_at"`
	ExpiresAt         time.Time          `json:"expires_at"`
	DecidedAt         *time.Time         `json:"decided_at"`
	ExecutedAt        *time.Time         `json:"executed_at"`
	DecisionNote      *string            `json:"decision_note"`
	PayloadHash       string             `json:"payload_hash"`
	ExecutorSupported bool               `json:"executor_supported"`
	Snapshot          map[string]*string `json:"snapshot,omitempty"`
	payload           []byte
}
type ApprovalList struct {
	Items    []Approval `json:"items"`
	Total    string     `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}
type ApprovalFilter struct {
	Page, PageSize int
	Scope, Status  string
}
type ApprovalProof struct {
	Principal                                    Principal
	Token, RequestHash, RequestID, IP, UserAgent string
}
type ApprovalAuditor interface {
	Append(context.Context, pgx.Tx, audit.Event) error
}
type ApprovalService struct {
	Auth  *Service
	Audit ApprovalAuditor
	// FundPubKey 是资金系统的 Ed25519 公钥（FUND_ADDRESS_PUBKEY_B64 的原值）。
	// 收款地址导入的执行器要用它验签；留空时那个动作会拒绝执行，其余动作不受影响。
	FundPubKey string
}

const approvalColumns = `id::text,action_code,target_type,target_id::text,status,reason,requested_by::text,decided_by::text,requested_at,expires_at,decided_at,executed_at,decision_note,payload_hash,payload`

func scanApproval(row pgx.Row) (a Approval, err error) {
	err = row.Scan(&a.ID, &a.ActionCode, &a.TargetType, &a.TargetID, &a.Status, &a.Reason, &a.RequestedBy, &a.DecidedBy, &a.CreatedAt, &a.ExpiresAt, &a.DecidedAt, &a.ExecutedAt, &a.DecisionNote, &a.PayloadHash, &a.payload)
	return
}
func (a *Approval) present(now time.Time, details bool) {
	if a.Status == "PENDING" && !a.ExpiresAt.After(now) {
		a.Status = "EXPIRED"
	}
	_, supported := executorFor(a.ActionCode, a.TargetType)
	a.ExecutorSupported = supported
	// Only explicitly allowlisted public snapshots are exposed; private payloads stay hidden.
	if details && a.ActionCode == publishAction {
		if snapshot, err := decodeVersionSnapshot(a.payload); err == nil && snapshot.hash() == a.PayloadHash && a.TargetID != nil && snapshot.ID == *a.TargetID {
			a.Snapshot = snapshot.fields()
		}
	}
	if details && (a.ActionCode == actionAdminCreate || a.ActionCode == actionAdminRoles) {
		a.Snapshot = adminManagementFields(a.payload, a.PayloadHash)
	}
	a.payload = nil
}
func positiveApprovalID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, httpx.NewError(httpx.ErrBadRequest, "编号格式无效")
	}
	return id, nil
}
func approvalText(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 500 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validApprovalStatus(s string) bool {
	switch s {
	case "", "PENDING", "APPROVED", "REJECTED", "EXECUTED", "EXPIRED", "FAILED":
		return true
	}
	return false
}
func normalizeApprovalFilter(f ApprovalFilter) (ApprovalFilter, error) {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 20
	}
	if f.Scope == "" {
		f.Scope = "pending"
	}
	if f.Page < 1 || f.Page > 1000000 || f.PageSize < 1 || f.PageSize > 50 || (f.Scope != "all" && f.Scope != "mine" && f.Scope != "pending") || !validApprovalStatus(f.Status) {
		return f, httpx.NewError(httpx.ErrBadRequest, "审批查询参数无效")
	}
	return f, nil
}
func (s *ApprovalService) readAuthority(ctx context.Context, p Principal, permission string) error {
	id, err := positiveApprovalID(p.ID)
	if err != nil || p.SessionID <= 0 {
		return errSession
	}
	var ok bool
	err = s.Auth.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users u JOIN admin_sessions s ON s.admin_user_id=u.id WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$3) AND s.id=$2 AND s.revoked_at IS NULL AND s.expires_at>$3 AND s.last_active_at>$3-interval '30 minutes' AND EXISTS(SELECT 1 FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id WHERE ur.admin_user_id=u.id AND rp.permission_code=$4))`, id, p.SessionID, s.Auth.Clock.Now(), permission).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NewError(httpx.ErrForbidden, "无权执行此操作")
	}
	return nil
}
func (s *ApprovalService) List(ctx context.Context, p Principal, f ApprovalFilter) (ApprovalList, error) {
	var out ApprovalList
	f, err := normalizeApprovalFilter(f)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = s.readAuthority(ctx, p, "approvals.read"); err != nil {
		return out, err
	}
	now := s.Auth.Clock.Now()
	where := ` WHERE ($1='all' OR ($1='mine' AND requested_by=$2::bigint) OR ($1='pending' AND requested_by<>$2::bigint AND status='PENDING' AND expires_at>$3)) AND ($4='' OR CASE WHEN status='PENDING' AND expires_at<=$3 THEN 'EXPIRED' ELSE status END=$4)`
	var total int64
	if err = s.Auth.Pool.QueryRow(ctx, `SELECT count(*) FROM admin_approvals`+where, f.Scope, p.ID, now, f.Status).Scan(&total); err != nil {
		return out, err
	}
	rows, err := s.Auth.Pool.Query(ctx, `SELECT `+approvalColumns+` FROM admin_approvals`+where+` ORDER BY admin_approvals.requested_at DESC,admin_approvals.id DESC LIMIT $5 OFFSET $6`, f.Scope, p.ID, now, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out = ApprovalList{Items: []Approval{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize}
	for rows.Next() {
		a, e := scanApproval(rows)
		if e != nil {
			return ApprovalList{}, e
		}
		a.present(now, false)
		out.Items = append(out.Items, a)
	}
	return out, rows.Err()
}
func (s *ApprovalService) Get(ctx context.Context, p Principal, id int64) (Approval, error) {
	if id <= 0 {
		return Approval{}, httpx.NewError(httpx.ErrBadRequest, "编号格式无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.readAuthority(ctx, p, "approvals.read"); err != nil {
		return Approval{}, err
	}
	a, err := scanApproval(s.Auth.Pool.QueryRow(ctx, `SELECT `+approvalColumns+` FROM admin_approvals WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, httpx.NewError(httpx.ErrNotFound, "审批单不存在")
	}
	if err == nil {
		a.present(s.Auth.Clock.Now(), true)
	}
	return a, err
}

func lockApprovalUsers(ctx context.Context, tx pgx.Tx, actor, author int64) error {
	rows, err := tx.Query(ctx, `SELECT id FROM admin_users WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`, actor, author)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}
func lockApprovalPermission(ctx context.Context, tx pgx.Tx, id int64, action string, risk int, now time.Time) error {
	var allowed int
	err := tx.QueryRow(ctx, `SELECT p.risk_level FROM admin_users u JOIN admin_user_roles ur ON ur.admin_user_id=u.id JOIN admin_role_permissions rp ON rp.role_id=ur.role_id JOIN admin_permissions p ON p.code=rp.permission_code WHERE u.id=$1 AND u.status='ACTIVE' AND (u.locked_until IS NULL OR u.locked_until<=$4) AND p.code=$2 AND p.risk_level=$3 FOR SHARE OF ur,rp,p`, id, action, risk, now).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NewError(httpx.ErrForbidden, "管理员状态或权限已变化")
	}
	return err
}
func (s *ApprovalService) freshProof(ctx context.Context, tx pgx.Tx, p ApprovalProof) error {
	if err := s.Auth.stepUpSessionFresh(ctx, tx, p.Principal); err != nil {
		return err
	}
	var expiry time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at FROM admin_step_up_tokens WHERE token_hash=$1`, tokenHash(p.Token)).Scan(&expiry); err != nil {
		return err
	}
	if !expiry.After(s.Auth.Clock.Now()) {
		return errStepUp
	}
	return nil
}
func (s *ApprovalService) auditEvent(p ApprovalProof, action, target string, id int64) audit.Event {
	actor, _ := positiveApprovalID(p.Principal.ID)
	e := audit.Event{ActionCode: action, TargetType: target, RequestID: p.RequestID, IP: p.IP, UserAgent: truncate(p.UserAgent, 255), Result: "SUCCESS"}
	if actor > 0 {
		e.AdminUserID = &actor
	}
	if id > 0 {
		e.TargetID = &id
	}
	return e
}
func approvalAuditData(a Approval, note bool) json.RawMessage {
	data := map[string]any{"approval_id": a.ID, "status": a.Status, "action_code": a.ActionCode, "target_type": a.TargetType, "target_id": a.TargetID}
	if note {
		data["decision_note"] = a.DecisionNote
	}
	raw, _ := json.Marshal(data)
	return raw
}

// A business rollback must release every lock before appending its denial in a
// separate transaction. Failure to persist that security event returns 500.
func (s *ApprovalService) RecordFailure(ctx context.Context, e audit.Event, operationErr error) error {
	e.Result = "FAILED"
	e.BeforeData = nil
	e.AfterData = nil
	var apiErr *httpx.Error
	code := httpx.ErrInternal
	if errors.As(operationErr, &apiErr) {
		code = apiErr.Code
		if code.HTTPStatus() < 500 {
			e.Result = "DENIED"
		}
	}
	e.AfterData, _ = json.Marshal(map[string]string{"error_code": strconv.Itoa(int(code))})
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return errors.New("approval security audit unavailable")
	}
	defer rollbackApproval(tx)
	if err = s.Audit.Append(c, tx, e); err == nil {
		err = tx.Commit(c)
	}
	if err != nil {
		return errors.New("approval security audit unavailable")
	}
	if apiErr == nil {
		return httpx.NewError(httpx.ErrInternal, "审批处理失败，请刷新核实")
	}
	return operationErr
}
func (s *ApprovalService) mutate(ctx context.Context, p ApprovalProof, e audit.Event, fn func(context.Context, pgx.Tx, *audit.Event) (Approval, error)) (out Approval, err error) {
	if s == nil || s.Auth == nil || s.Auth.Pool == nil || s.Auth.Clock == nil || s.Audit == nil {
		return out, errors.New("approval dependencies unavailable")
	}
	c, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := s.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, s.RecordFailure(ctx, e, err)
	}
	defer rollbackApproval(tx)
	// All approval mutations share this lock. User locks follow ID order, then
	// current-session/permission/token, approval/version, and finally audit chain.
	_, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended('admin-approval-write:v1',0))`)
	if err == nil {
		out, err = fn(c, tx, &e)
	}
	if err == nil {
		err = s.Audit.Append(c, tx, e)
	}
	// Audit can itself wait on the chain mutex. No deadline may be extended by
	// waiting for this final lock before the transaction commits.
	if err == nil && e.StepUpVerified {
		err = s.freshProof(c, tx, p)
		if err == nil && !out.ExpiresAt.After(s.Auth.Clock.Now()) {
			err = httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
		}
	}
	if err == nil {
		err = tx.Commit(c)
	}
	if err != nil {
		rollbackApproval(tx)
		return Approval{}, s.RecordFailure(ctx, e, err)
	}
	out.present(s.Auth.Clock.Now(), true)
	return out, nil
}
func rollbackApproval(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
