package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yuhai-vpn/server/internal/fieldenc"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const (
	adminRead             = "admins.read"
	actionAdminCreate     = "admins.create"
	actionAdminRoles      = "admins.roles"
	adminManagementTarget = "admin_user"
)

var adminUsername = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

type AdminManagementService struct{ Security *ApprovalService }
type ManagedAdmin struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Status      string     `json:"status"`
	RoleIDs     []string   `json:"role_ids"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at"`
}
type ManagedAdminRole struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}
type ManagedAdminPage struct {
	Items    []ManagedAdmin     `json:"items"`
	Roles    []ManagedAdminRole `json:"roles"`
	Total    string             `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
}
type ManagedAdminFilter struct {
	Page, PageSize   int
	Username, Status string
}
type AdminCreateInput struct {
	OperationID string   `json:"operation_id"`
	Reason      string   `json:"reason"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	TOTPSecret  string   `json:"totp_secret"`
	TOTPCode    string   `json:"totp_code"`
	RoleIDs     []string `json:"role_ids"`
}
type AdminRolesInput struct {
	OperationID string   `json:"operation_id"`
	Reason      string   `json:"reason"`
	RoleIDs     []string `json:"role_ids"`
}

// Public snapshot binds both the proposed roles and the previous target state.
// No password hash, TOTP seed, or encrypted credential appears in this object.
type adminManagementSnapshot struct {
	OperationID    string             `json:"operation_id"`
	Username       string             `json:"username"`
	TargetID       string             `json:"target_id,omitempty"`
	Before         []ManagedAdminRole `json:"before"`
	Roles          []ManagedAdminRole `json:"roles"`
	CredentialHash string             `json:"credential_hash,omitempty"`
}
type adminProvisioningCredentials struct {
	PasswordHash string `json:"password_hash"`
	TOTPSecret   string `json:"totp_secret"`
	TOTPLastStep int64  `json:"totp_last_step"`
}

func managementHash(raw []byte) string          { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (s adminManagementSnapshot) bytes() []byte { b, _ := json.Marshal(s); return b }
func managementStale() error {
	return httpx.NewError(httpx.ErrApprovalStale, "管理员或角色资料已变化，请重新发起")
}

func (s *AdminManagementService) List(ctx context.Context, p Principal, f ManagedAdminFilter) (out ManagedAdminPage, err error) {
	if s == nil || s.Security == nil || s.Security.Auth == nil || s.Security.Auth.Pool == nil || s.Security.Auth.Clock == nil {
		return out, httpx.Internal
	}
	f.Page, f.PageSize, err = normalizeUserPage(f.Page, f.PageSize)
	if err != nil {
		return out, err
	}
	if f.Username != "" && !nodeText(f.Username, 32, false) || f.Status != "" && f.Status != "ACTIVE" && f.Status != "LOCKED" && f.Status != "DISABLED" {
		return out, httpx.NewError(httpx.ErrBadRequest, "管理员查询参数无效")
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	auth := &UserReadService{Auth: s.Security.Auth}
	if err = auth.authority(c, p, []string{adminRead}); err != nil {
		return out, err
	}
	tx, err := s.Security.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, err
	}
	defer rollbackApproval(tx)
	out = ManagedAdminPage{Items: []ManagedAdmin{}, Roles: []ManagedAdminRole{}, Page: f.Page, PageSize: f.PageSize}
	if err = tx.QueryRow(c, `SELECT count(*)::text FROM admin_users WHERE ($1='' OR username=$1) AND ($2='' OR status=$2)`, f.Username, f.Status).Scan(&out.Total); err != nil {
		return ManagedAdminPage{}, err
	}
	rows, err := tx.Query(c, `SELECT u.id::text,u.username,u.status,ARRAY(SELECT ur.role_id::text FROM admin_user_roles ur WHERE ur.admin_user_id=u.id ORDER BY ur.role_id),u.created_at,u.updated_at,u.last_login_at FROM admin_users u WHERE ($1='' OR username=$1) AND ($2='' OR status=$2) ORDER BY u.id DESC LIMIT $3 OFFSET $4`, f.Username, f.Status, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return ManagedAdminPage{}, err
	}
	for rows.Next() {
		var u ManagedAdmin
		if err = rows.Scan(&u.ID, &u.Username, &u.Status, &u.RoleIDs, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
			rows.Close()
			return ManagedAdminPage{}, err
		}
		out.Items = append(out.Items, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ManagedAdminPage{}, err
	}
	rows, err = tx.Query(c, `SELECT r.id::text,r.code,r.name,ARRAY(SELECT permission_code FROM admin_role_permissions WHERE role_id=r.id ORDER BY permission_code) FROM admin_roles r ORDER BY r.id`)
	if err != nil {
		return ManagedAdminPage{}, err
	}
	for rows.Next() {
		var role ManagedAdminRole
		if err = rows.Scan(&role.ID, &role.Code, &role.Name, &role.Permissions); err != nil {
			rows.Close()
			return ManagedAdminPage{}, err
		}
		out.Roles = append(out.Roles, role)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ManagedAdminPage{}, err
	}
	if err = tx.Commit(c); err != nil {
		return ManagedAdminPage{}, err
	}
	if err = auth.authority(c, p, []string{adminRead}); err != nil {
		return ManagedAdminPage{}, err
	}
	return out, nil
}

func canonicalAdminRoleIDs(ids []string, allowEmpty bool) ([]int64, error) {
	if ids == nil || len(ids) > 20 || !allowEmpty && len(ids) == 0 {
		return nil, httpx.NewError(httpx.ErrBadRequest, "role_ids 必须是最多20个不重复角色编号")
	}
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, raw := range ids {
		id, err := positiveApprovalID(raw)
		if err != nil || seen[id] {
			return nil, httpx.NewError(httpx.ErrBadRequest, "角色编号无效或重复")
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
func loadManagedRoles(ctx context.Context, tx pgx.Tx, ids []int64) ([]ManagedAdminRole, error) {
	out := make([]ManagedAdminRole, 0, len(ids))
	// FOR UPDATE on the role also conflicts with new permission rows referencing
	// it through the foreign key. Existing permission rows are locked below;
	// additions, removals and metadata edits cannot change a granted role midway.
	for _, id := range ids {
		var role ManagedAdminRole
		err := tx.QueryRow(ctx, `SELECT id::text,code,name FROM admin_roles WHERE id=$1 FOR UPDATE`, id).Scan(&role.ID, &role.Code, &role.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NewError(httpx.ErrBadRequest, "角色不存在")
		}
		if err != nil {
			return nil, err
		}
		role.Permissions = []string{}
		rows, err := tx.Query(ctx, `SELECT permission_code FROM admin_role_permissions WHERE role_id=$1 ORDER BY permission_code FOR SHARE`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var code string
			if err = rows.Scan(&code); err != nil {
				rows.Close()
				return nil, err
			}
			role.Permissions = append(role.Permissions, code)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, nil
}
func currentManagedRoles(ctx context.Context, tx pgx.Tx, id int64) ([]ManagedAdminRole, error) {
	var ids []int64
	err := tx.QueryRow(ctx, `SELECT ARRAY(SELECT role_id FROM admin_user_roles WHERE admin_user_id=$1 ORDER BY role_id)`, id).Scan(&ids)
	if err != nil {
		return nil, err
	}
	return loadManagedRoles(ctx, tx, ids)
}
func requireManageableRoles(ctx context.Context, tx pgx.Tx, actor int64, groups ...[]ManagedAdminRole) error {
	rows, err := tx.Query(ctx, `SELECT rp.permission_code FROM admin_user_roles ur JOIN admin_role_permissions rp ON rp.role_id=ur.role_id WHERE ur.admin_user_id=$1 FOR SHARE OF ur,rp`, actor)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			rows.Close()
			return err
		}
		held[code] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, roles := range groups {
		for _, role := range roles {
			for _, code := range role.Permissions {
				if !held[code] {
					return httpx.NewError(httpx.ErrForbidden, "不能管理超出本人权限范围的角色")
				}
			}
		}
	}
	return nil
}

func (s *AdminManagementService) RequestCreate(ctx context.Context, p ApprovalProof, in AdminCreateInput) (Approval, error) {
	return s.request(ctx, p, actionAdminCreate, 0, in.OperationID, in.Reason, func(c context.Context, tx pgx.Tx, actor int64) (adminManagementSnapshot, []byte, error) {
		snap := adminManagementSnapshot{OperationID: in.OperationID, Username: in.Username, Before: []ManagedAdminRole{}}
		if !adminUsername.MatchString(in.Username) || !utf8.ValidString(in.Password) || utf8.RuneCountInString(in.Password) < 12 || len(in.Password) > 1024 {
			return snap, nil, httpx.NewError(httpx.ErrBadRequest, "用户名或初始密码不符合要求")
		}
		ids, err := canonicalAdminRoleIDs(in.RoleIDs, false)
		if err != nil {
			return snap, nil, err
		}
		snap.Roles, err = loadManagedRoles(c, tx, ids)
		if err != nil {
			return snap, nil, err
		}
		if err = requireManageableRoles(c, tx, actor, snap.Roles); err != nil {
			return snap, nil, err
		}
		var exists bool
		if err = tx.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM admin_users WHERE username=$1)`, in.Username).Scan(&exists); err != nil {
			return snap, nil, err
		}
		if exists {
			return snap, nil, httpx.NewError(httpx.ErrConflict, "管理员用户名已存在")
		}
		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(in.TOTPSecret)
		if err != nil || len(raw) < 20 || len(raw) > 64 || base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw) != in.TOTPSecret {
			return snap, nil, httpx.NewError(httpx.ErrBadRequest, "TOTP 种子格式无效")
		}
		step, ok := verifyTOTP(in.TOTPSecret, in.TOTPCode, s.Security.Auth.Clock.Now(), 0)
		if !ok {
			return snap, nil, httpx.NewError(httpx.ErrTOTPInvalid, "新管理员的动态口令无效")
		}
		hash, err := HashPassword(in.Password)
		if err != nil {
			return snap, nil, httpx.Internal
		}
		secret, _ := json.Marshal(adminProvisioningCredentials{PasswordHash: hash, TOTPSecret: in.TOTPSecret, TOTPLastStep: step})
		enc, err := s.Security.Auth.Enc.Encrypt(secret, fieldenc.AAD("admin_management_requests", "credentials_enc", in.OperationID))
		if err != nil {
			return snap, nil, err
		}
		snap.CredentialHash = managementHash(enc)
		return snap, enc, nil
	})
}
func (s *AdminManagementService) RequestRoles(ctx context.Context, p ApprovalProof, id int64, in AdminRolesInput) (Approval, error) {
	return s.request(ctx, p, actionAdminRoles, id, in.OperationID, in.Reason, func(c context.Context, tx pgx.Tx, actor int64) (adminManagementSnapshot, []byte, error) {
		snap := adminManagementSnapshot{OperationID: in.OperationID, TargetID: adminID(id)}
		if id <= 0 {
			return snap, nil, httpx.NewError(httpx.ErrBadRequest, "管理员编号无效")
		}
		if id == actor {
			return snap, nil, httpx.NewError(httpx.ErrForbidden, "不能修改自己的角色")
		}
		ids, err := canonicalAdminRoleIDs(in.RoleIDs, true)
		if err != nil {
			return snap, nil, err
		}
		if err = tx.QueryRow(c, `SELECT username FROM admin_users WHERE id=$1 FOR UPDATE`, id).Scan(&snap.Username); errors.Is(err, pgx.ErrNoRows) {
			return snap, nil, httpx.NewError(httpx.ErrNotFound, "管理员不存在")
		}
		if err != nil {
			return snap, nil, err
		}
		snap.Before, err = currentManagedRoles(c, tx, id)
		if err != nil {
			return snap, nil, err
		}
		snap.Roles, err = loadManagedRoles(c, tx, ids)
		if err != nil {
			return snap, nil, err
		}
		if err = requireManageableRoles(c, tx, actor, snap.Before, snap.Roles); err != nil {
			return snap, nil, err
		}
		return snap, nil, nil
	})
}

// L3 request transaction: authorization, single-use proof, idempotency, encrypted
// credential staging, approval creation and audit commit together. Replay is
// scoped to the originating actor AND session and never adds a success audit.
func (s *AdminManagementService) request(ctx context.Context, p ApprovalProof, action string, target int64, operation, reason string,
	build func(context.Context, pgx.Tx, int64) (adminManagementSnapshot, []byte, error)) (out Approval, err error) {
	if s == nil || s.Security == nil || s.Security.Auth == nil || s.Security.Auth.Enc == nil || s.Security.Auth.Pool == nil || s.Security.Auth.Clock == nil || s.Security.Audit == nil {
		return out, httpx.Internal
	}
	a := s.Security
	e := a.auditEvent(p, action, adminManagementTarget, target)
	e.UserAgent = ""
	defer func() {
		if err != nil {
			err = a.RecordFailure(ctx, e, err)
		}
	}()
	if !validOperationID(operation) || !approvalText(reason) {
		return out, httpx.NewError(httpx.ErrBadRequest, "操作编号或理由无效")
	}
	if p.Token == "" {
		return out, httpx.NewError(httpx.ErrStepUpRequired, "请先完成二次验证")
	}
	if !validToken(p.Token) || !stepUpHash.MatchString(p.RequestHash) {
		return out, errStepUp
	}
	c, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := a.Auth.Pool.BeginTx(c, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer rollbackApproval(tx)
	if _, err = tx.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended('admin-approval-write:v1',0))`); err != nil {
		return out, err
	}
	actor, err := a.Auth.lockStepUpAuthority(c, tx, p.Principal, action, p.IP)
	if err != nil {
		return out, err
	}
	if err = lockApprovalPermission(c, tx, actor.id, action, 3, a.Auth.Clock.Now()); err != nil {
		return out, err
	}
	var savedActor, savedSession, approvalID int64
	var savedHash, savedProof, savedAction string
	err = tx.QueryRow(c, `SELECT admin_user_id,session_id,action_code,request_hash,proof_hash,approval_id FROM admin_management_requests WHERE operation_id=$1`, operation).Scan(&savedActor, &savedSession, &savedAction, &savedHash, &savedProof, &approvalID)
	if err == nil {
		if savedActor != actor.id || savedSession != p.Principal.SessionID || savedAction != action || savedHash != p.RequestHash {
			return out, httpx.NewError(httpx.ErrConflict, "该 operation_id 已用于不同的请求或会话")
		}
		if savedProof != tokenHash(p.Token) {
			if err = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, action, p.RequestHash, p.IP); err != nil {
				return out, err
			}
		}
		if err = a.freshProof(c, tx, p); err != nil {
			return out, err
		}
		out, err = scanApproval(tx.QueryRow(c, `SELECT `+approvalColumns+` FROM admin_approvals WHERE id=$1`, approvalID))
		if err != nil {
			return out, err
		}
		if err = tx.Commit(c); err != nil {
			return Approval{}, err
		}
		out.present(a.Auth.Clock.Now(), true)
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, action, p.RequestHash, p.IP); err != nil {
		return out, err
	}
	e.StepUpVerified = true
	snap, credentials, err := build(c, tx, actor.id)
	if err != nil {
		return out, err
	}
	now := a.Auth.Clock.Now()
	payload := snap.bytes()
	var targetID *int64
	if target > 0 {
		targetID = &target
	}
	out, err = scanApproval(tx.QueryRow(c, `INSERT INTO admin_approvals(action_code,target_type,target_id,payload,payload_hash,reason,requested_by,requested_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+approvalColumns, action, adminManagementTarget, targetID, payload, managementHash(payload), reason, actor.id, now, now.Add(approvalTTL)))
	if err != nil {
		return out, err
	}
	approvalID, _ = positiveApprovalID(out.ID)
	if _, err = tx.Exec(c, `INSERT INTO admin_management_requests(operation_id,admin_user_id,session_id,action_code,request_hash,proof_hash,approval_id,credentials_enc,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, operation, actor.id, p.Principal.SessionID, action, p.RequestHash, tokenHash(p.Token), approvalID, credentials, now); err != nil {
		return Approval{}, err
	}
	e.ApprovalID = &approvalID
	e.AfterData = approvalAuditData(out, false)
	if err = a.Audit.Append(c, tx, e); err != nil {
		return Approval{}, err
	}
	if err = a.freshProof(c, tx, p); err != nil {
		return Approval{}, err
	}
	if err = tx.Commit(c); err != nil {
		return Approval{}, err
	}
	out.present(a.Auth.Clock.Now(), true)
	return out, nil
}

func executeAdminManagement(ctx context.Context, s *ApprovalService, tx pgx.Tx, p ApprovalProof, a Approval, approvalID int64, now time.Time) error {
	var snap adminManagementSnapshot
	if len(a.payload) > 65536 || decodeApprovalObject(a.payload, &snap) != nil || managementHash(snap.bytes()) != a.PayloadHash || !validOperationID(snap.OperationID) || !nodeText(snap.Username, 32, false) {
		return managementStale()
	}
	actor, err := positiveApprovalID(p.Principal.ID)
	if err != nil {
		return errSession
	}
	author, err := positiveApprovalID(a.RequestedBy)
	if err != nil {
		return managementStale()
	}
	var enc []byte
	var savedAction string
	if err = tx.QueryRow(ctx, `SELECT credentials_enc,action_code FROM admin_management_requests WHERE operation_id=$1 AND approval_id=$2 AND admin_user_id=$3 FOR UPDATE`, snap.OperationID, approvalID, author).Scan(&enc, &savedAction); err != nil {
		return managementStale()
	}
	if savedAction != a.ActionCode {
		return managementStale()
	}
	ids := make([]int64, 0, len(snap.Roles))
	for _, r := range snap.Roles {
		id, err := positiveApprovalID(r.ID)
		if err != nil {
			return managementStale()
		}
		ids = append(ids, id)
	}
	current, err := loadManagedRoles(ctx, tx, ids)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(current)
	expected, _ := json.Marshal(snap.Roles)
	if string(b) != string(expected) {
		return managementStale()
	}
	for _, who := range []int64{actor, author} {
		if err = requireManageableRoles(ctx, tx, who, snap.Before, snap.Roles); err != nil {
			return err
		}
	}
	if err = s.freshProof(ctx, tx, p); err != nil {
		return err
	}
	if a.ActionCode == actionAdminCreate {
		if !adminUsername.MatchString(snap.Username) || a.TargetID != nil || snap.TargetID != "" || len(snap.Before) != 0 || len(snap.Roles) == 0 || managementHash(enc) != snap.CredentialHash || s.Auth.Enc == nil {
			return managementStale()
		}
		plain, err := s.Auth.Enc.Decrypt(enc, fieldenc.AAD("admin_management_requests", "credentials_enc", snap.OperationID))
		if err != nil {
			return managementStale()
		}
		var credentials adminProvisioningCredentials
		if decodeApprovalObject(plain, &credentials) != nil {
			return managementStale()
		}
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO admin_users(username,password_hash,totp_secret_enc,totp_last_step,created_at,updated_at) VALUES($1,$2,''::bytea,$3,$4,$4) RETURNING id`, snap.Username, credentials.PasswordHash, credentials.TOTPLastStep, now).Scan(&id)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return httpx.NewError(httpx.ErrConflict, "管理员用户名已存在")
		}
		if err != nil {
			return err
		}
		encrypted, err := s.Auth.Enc.Encrypt([]byte(credentials.TOTPSecret), fieldenc.AAD("admin_users", "totp_secret_enc", id))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE admin_users SET totp_secret_enc=$2 WHERE id=$1`, id, encrypted); err != nil {
			return err
		}
		for _, role := range snap.Roles {
			if _, err = tx.Exec(ctx, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, id, role.ID); err != nil {
				return err
			}
		}
		// Record the created account on the completed approval without exposing secrets.
		_, err = tx.Exec(ctx, `UPDATE admin_approvals SET target_id=$2 WHERE id=$1`, approvalID, id)
		return err
	}
	if a.ActionCode != actionAdminRoles || a.TargetID == nil || *a.TargetID != snap.TargetID || len(enc) != 0 || snap.CredentialHash != "" {
		return managementStale()
	}
	target, err := positiveApprovalID(snap.TargetID)
	if err != nil {
		return managementStale()
	}
	if target == actor || target == author {
		return httpx.NewError(httpx.ErrForbidden, "不能发起或审批自己的角色变更")
	}
	var username string
	if err = tx.QueryRow(ctx, `SELECT username FROM admin_users WHERE id=$1 FOR UPDATE`, target).Scan(&username); err != nil {
		return err
	}
	if username != snap.Username {
		return managementStale()
	}
	before, err := currentManagedRoles(ctx, tx, target)
	if err != nil {
		return err
	}
	b, _ = json.Marshal(before)
	expected, _ = json.Marshal(snap.Before)
	if string(b) != string(expected) {
		return managementStale()
	}
	if _, err = tx.Exec(ctx, `DELETE FROM admin_user_roles WHERE admin_user_id=$1`, target); err != nil {
		return err
	}
	for _, role := range snap.Roles {
		if _, err = tx.Exec(ctx, `INSERT INTO admin_user_roles(admin_user_id,role_id) VALUES($1,$2)`, target, role.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE admin_users SET updated_at=$2 WHERE id=$1`, target, now); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2 WHERE admin_user_id=$1 AND revoked_at IS NULL`, target, now)
	return err
}

// Public approval details allow reviewers to verify the exact account and roles.
func adminManagementFields(raw []byte, hash string) map[string]*string {
	var snap adminManagementSnapshot
	if decodeApprovalObject(raw, &snap) != nil || managementHash(snap.bytes()) != hash {
		return nil
	}
	fields := map[string]*string{"username": &snap.Username, "operation_id": &snap.OperationID}
	roles := make([]string, 0, len(snap.Roles))
	for _, r := range snap.Roles {
		roles = append(roles, r.Code+" ("+r.ID+")")
	}
	value := string(mustAdminJSON(roles))
	fields["roles"] = &value
	old := make([]string, 0, len(snap.Before))
	for _, r := range snap.Before {
		old = append(old, r.Code+" ("+r.ID+")")
	}
	before := string(mustAdminJSON(old))
	fields["previous_roles"] = &before
	return fields
}
func mustAdminJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
