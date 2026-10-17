package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yuhai-vpn/server/internal/httpx"
)

const versionRead = "app_versions.read"
const versionDraft = "app_versions.draft"

type VersionService struct{ Approvals *ApprovalService }
type VersionDetail struct {
	versionSnapshot
	CreatedAt         time.Time `json:"created_at"`
	SnapshotHash      string    `json:"snapshot_hash"`
	PendingApprovalID *string   `json:"pending_approval_id"`
	CanEdit           bool      `json:"can_edit"`
}
type VersionList struct {
	Items    []VersionDetail `json:"items"`
	Total    string          `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}
type VersionFilter struct {
	Page, PageSize                int
	Platform, Status, VersionCode string
}
type VersionDraftFields struct {
	Platform    string  `json:"platform"`
	VersionName string  `json:"version_name"`
	VersionCode string  `json:"version_code"`
	Minimum     string  `json:"min_supported_code"`
	DownloadURL string  `json:"download_url"`
	SHA256      string  `json:"file_sha256"`
	CertSHA256  *string `json:"cert_sha256"`
	Notes       string  `json:"release_notes"`
}
type VersionCreate struct {
	VersionDraftFields
	OperationID string `json:"operation_id"`
}
type VersionUpdate struct {
	VersionCreate
	ExpectedSnapshotHash string `json:"expected_snapshot_hash"`
}

func (in VersionDraftFields) snapshot(id string) versionSnapshot {
	return versionSnapshot{ID: id, Platform: in.Platform, VersionName: in.VersionName, VersionCode: in.VersionCode, Minimum: in.Minimum, DownloadURL: &in.DownloadURL, SHA256: &in.SHA256, CertSHA256: in.CertSHA256, Notes: in.Notes, Status: "DRAFT"}
}
func (in VersionCreate) valid() bool {
	id, err := uuid.Parse(in.OperationID)
	if err != nil || id.Version() != 4 || id.Variant() != uuid.RFC4122 || id.String() != in.OperationID || !in.snapshot("1").valid() {
		return false
	}
	// Public release notes can be multiline, but controls cannot enter PostgreSQL
	// text fields or masquerade as display content.
	for _, r := range in.Notes {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

const versionReadColumns = `v.id::text,v.platform,v.version_name,v.version_code::text,v.min_supported_code::text,v.download_url,v.file_sha256,v.cert_sha256,COALESCE(v.release_notes,''),v.status,v.published_at::text,v.approval_id::text,v.created_at,
 (SELECT a.id::text FROM admin_approvals a WHERE a.action_code='app_versions.publish' AND a.target_type='app_version' AND a.target_id=v.id AND a.status='PENDING' AND a.expires_at>$1 ORDER BY a.id LIMIT 1)`

func scanVersionDetail(row pgx.Row) (v VersionDetail, err error) {
	err = row.Scan(&v.ID, &v.Platform, &v.VersionName, &v.VersionCode, &v.Minimum, &v.DownloadURL, &v.SHA256, &v.CertSHA256, &v.Notes, &v.Status, &v.PublishedAt, &v.ApprovalID, &v.CreatedAt, &v.PendingApprovalID)
	if err == nil {
		v.SnapshotHash = v.hash()
		v.CanEdit = v.Status == "DRAFT" && v.PendingApprovalID == nil && v.PublishedAt == nil && v.ApprovalID == nil
	}
	return
}
func normalizeVersionFilter(f VersionFilter) (VersionFilter, error) {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 20
	}
	valid := f.Page > 0 && f.Page <= 1000000 && f.PageSize > 0 && f.PageSize <= 50 && (f.Platform == "" || f.Platform == "ANDROID" || f.Platform == "IOS") && (f.Status == "" || f.Status == "DRAFT" || f.Status == "PUBLISHED" || f.Status == "WITHDRAWN")
	if f.VersionCode != "" {
		n, err := strconv.ParseInt(f.VersionCode, 10, 32)
		valid = valid && f.Platform != "" && err == nil && n > 0 && strconv.FormatInt(n, 10) == f.VersionCode
	}
	if !valid {
		return f, httpx.NewError(httpx.ErrBadRequest, "版本查询参数无效")
	}
	return f, nil
}
func (s *VersionService) List(ctx context.Context, p Principal, f VersionFilter) (VersionList, error) {
	var out VersionList
	f, err := normalizeVersionFilter(f)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a := s.Approvals
	if err = a.readAuthority(ctx, p, versionRead); err != nil {
		return out, err
	}
	args := []any{a.Auth.Clock.Now(), f.Platform, f.Status, f.VersionCode}
	where := ` WHERE ($2='' OR v.platform=$2) AND ($3='' OR v.status=$3) AND ($4='' OR v.version_code::text=$4)`
	var total int64
	// Keep the parameter numbering identical to the detail selection below.
	if err = a.Auth.Pool.QueryRow(ctx, `SELECT count(*) FROM app_versions v`+where+` AND $1::timestamptz IS NOT NULL`, args...).Scan(&total); err != nil {
		return out, err
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := a.Auth.Pool.Query(ctx, `SELECT `+versionReadColumns+` FROM app_versions v`+where+` ORDER BY v.created_at DESC,v.id DESC LIMIT $5 OFFSET $6`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out = VersionList{Items: []VersionDetail{}, Total: strconv.FormatInt(total, 10), Page: f.Page, PageSize: f.PageSize}
	for rows.Next() {
		v, err := scanVersionDetail(rows)
		if err != nil {
			return VersionList{}, err
		}
		out.Items = append(out.Items, v)
	}
	return out, rows.Err()
}
func (s *VersionService) Get(ctx context.Context, p Principal, id int64) (VersionDetail, error) {
	if id <= 0 {
		return VersionDetail{}, httpx.NewError(httpx.ErrBadRequest, "版本编号无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a := s.Approvals
	if err := a.readAuthority(ctx, p, versionRead); err != nil {
		return VersionDetail{}, err
	}
	v, err := scanVersionDetail(a.Auth.Pool.QueryRow(ctx, `SELECT `+versionReadColumns+` FROM app_versions v WHERE v.id=$2`, a.Auth.Clock.Now(), id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NewError(httpx.ErrNotFound, "版本不存在")
	}
	return v, err
}
func versionAuditData(v VersionDetail) json.RawMessage {
	// No raw request, operation UUID, URL, free-text notes/name, or auth material.
	raw, _ := json.Marshal(map[string]any{"target_id": v.ID, "platform": v.Platform, "version_code": v.VersionCode, "min_supported_code": v.Minimum, "status": v.Status, "file_sha256": v.SHA256, "cert_sha256": v.CertSHA256})
	return raw
}
func versionConflict(message string) error { return httpx.NewError(httpx.ErrConflict, message) }

// Save shares the approval mutex: publishing, editing, and approval decisions
// cannot invert version/approval locks or approve a concurrently edited draft.
// No automatic transaction retry is safe for a one-time step-up credential.
func (s *VersionService) Save(ctx context.Context, p ApprovalProof, id int64, in VersionUpdate) (out VersionDetail, err error) {
	a := s.Approvals
	if a == nil || a.Auth == nil || a.Auth.Pool == nil || a.Auth.Clock == nil || a.Audit == nil {
		return out, httpx.Internal
	}
	event := a.auditEvent(p, versionDraft, "app_version", id)
	event.UserAgent = ""
	defer func() {
		if err != nil {
			out = VersionDetail{}
			var dbErr *pgconn.PgError
			if errors.As(err, &dbErr) && dbErr.Code == "23505" {
				err = versionConflict("平台与版本号或操作编号已存在，请重新查询")
			}
			err = a.RecordFailure(ctx, event, err)
		}
	}()
	if id < 0 || !in.VersionCreate.valid() || (id == 0 && in.ExpectedSnapshotHash != "") || (id > 0 && !stepUpHash.MatchString(in.ExpectedSnapshotHash)) {
		return out, httpx.NewError(httpx.ErrBadRequest, "草稿资料或操作编号无效")
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
	actor, err := positiveApprovalID(p.Principal.ID)
	if err != nil {
		return out, errSession
	}
	if err = lockApprovalUsers(c, tx, actor, actor); err != nil {
		return out, err
	}
	if err = a.Auth.ConsumeStepUp(c, tx, p.Principal, p.Token, versionDraft, p.RequestHash, p.IP); err != nil {
		return out, err
	}
	event.StepUpVerified = true
	if err = lockApprovalPermission(c, tx, actor, versionDraft, 2, a.Auth.Clock.Now()); err != nil {
		return out, err
	}
	var previousHash, resultHash string
	var previousID int64
	err = tx.QueryRow(c, `SELECT request_hash,version_id,result_hash FROM admin_version_mutations WHERE admin_user_id=$1 AND operation_id=$2 FOR UPDATE`, actor, in.OperationID).Scan(&previousHash, &previousID, &resultHash)
	replay := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	err = nil
	if replay {
		if previousHash != p.RequestHash {
			return out, versionConflict("操作编号已用于不同请求")
		}
		// A fresh verified proof is still required for an intentional replay.
		if _, err = lockVersion(c, tx, previousID); err != nil {
			return out, err
		}
		out, err = scanVersionDetail(tx.QueryRow(c, `SELECT `+versionReadColumns+` FROM app_versions v WHERE v.id=$2`, a.Auth.Clock.Now(), previousID))
		if err != nil {
			return out, err
		}
		if out.SnapshotHash != resultHash {
			return out, versionConflict("此操作已完成，但版本已变化，请重新查询")
		}
	} else {
		code, _ := strconv.Atoi(in.VersionCode)
		minimum, _ := strconv.Atoi(in.Minimum)
		if id > 0 {
			if _, err = lockVersion(c, tx, id); err != nil {
				return out, err
			}
			before, e := scanVersionDetail(tx.QueryRow(c, `SELECT `+versionReadColumns+` FROM app_versions v WHERE v.id=$2`, a.Auth.Clock.Now(), id))
			if e != nil {
				return out, e
			}
			if !before.CanEdit {
				return out, versionConflict("仅可编辑没有有效待审批申请的草稿")
			}
			if before.SnapshotHash != in.ExpectedSnapshotHash {
				return out, httpx.NewError(httpx.ErrApprovalStale, "草稿已被修改，请刷新后重新编辑")
			}
			event.BeforeData = versionAuditData(before)
			var tag pgconn.CommandTag
			tag, err = tx.Exec(c, `UPDATE app_versions SET platform=$2,version_name=$3,version_code=$4,min_supported_code=$5,download_url=$6,file_sha256=$7,cert_sha256=$8,release_notes=$9 WHERE id=$1 AND status='DRAFT'`, id, in.Platform, in.VersionName, code, minimum, in.DownloadURL, in.SHA256, in.CertSHA256, in.Notes)
			if err == nil && tag.RowsAffected() != 1 {
				return out, versionConflict("版本状态已变化，请重新查询")
			}
		} else {
			err = tx.QueryRow(c, `INSERT INTO app_versions(platform,version_name,version_code,min_supported_code,download_url,file_sha256,cert_sha256,release_notes,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'DRAFT',$9) RETURNING id`, in.Platform, in.VersionName, code, minimum, in.DownloadURL, in.SHA256, in.CertSHA256, in.Notes, a.Auth.Clock.Now()).Scan(&id)
		}
		if err != nil {
			return out, err
		}
		out, err = scanVersionDetail(tx.QueryRow(c, `SELECT `+versionReadColumns+` FROM app_versions v WHERE v.id=$2`, a.Auth.Clock.Now(), id))
		if err != nil {
			return out, err
		}
		if _, err = tx.Exec(c, `INSERT INTO admin_version_mutations(admin_user_id,operation_id,request_hash,version_id,result_hash,created_at) VALUES($1,$2,$3,$4,$5,$6)`, actor, in.OperationID, p.RequestHash, id, out.SnapshotHash, a.Auth.Clock.Now()); err != nil {
			return out, err
		}
	}
	target, _ := positiveApprovalID(out.ID)
	event.TargetID = &target
	event.AfterData = versionAuditData(out)
	if err = a.Audit.Append(c, tx, event); err != nil {
		return out, err
	}
	if err = a.freshProof(c, tx, p); err != nil {
		return out, err
	}
	if err = tx.Commit(c); err != nil {
		return out, err
	}
	return out, nil
}
