package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuhai-vpn/server/internal/audit"
	"github.com/yuhai-vpn/server/internal/httpx"
)

type versionSnapshot struct {
	ID          string  `json:"id"`
	Platform    string  `json:"platform"`
	VersionName string  `json:"version_name"`
	VersionCode string  `json:"version_code"`
	Minimum     string  `json:"min_supported_code"`
	DownloadURL *string `json:"download_url"`
	SHA256      *string `json:"file_sha256"`
	CertSHA256  *string `json:"cert_sha256"`
	Notes       string  `json:"release_notes"`
	Status      string  `json:"status"`
	PublishedAt *string `json:"published_at"`
	ApprovalID  *string `json:"approval_id"`
}

func (v versionSnapshot) bytes() []byte { b, _ := json.Marshal(v); return b }
func (v versionSnapshot) hash() string {
	h := sha256.Sum256(v.bytes())
	return hex.EncodeToString(h[:])
}
func (v versionSnapshot) fields() map[string]*string {
	var out map[string]*string
	_ = json.Unmarshal(v.bytes(), &out)
	return out
}
func safePublicDownload(raw string) bool {
	if len(raw) > 512 || raw != strings.TrimSpace(raw) || strings.Contains(raw, "\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasSuffix(host, ".") {
		return false
	}
	// Installers use stable HTTPS hostnames. IsGlobalUnicast alone includes
	// documentation/benchmark/reserved ranges; refusing all IP literals avoids
	// an incomplete special-use allowlist. This performs no network fetch.
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	if !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".lan", ".home", ".test", ".invalid"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	// Public hostnames only; no browser-specific numeric IPv4 aliases, DNS
	// lookups or outbound requests during approval. DNS ownership is operational.
	if len(host) > 253 {
		return false
	}
	allNumeric := true
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			allNumeric = false
		}
	}
	if allNumeric {
		return false
	}
	last := host[strings.LastIndex(host, ".")+1:]
	decimalSuffix := true
	for _, r := range last {
		if r < '0' || r > '9' {
			decimalSuffix = false
		}
	}
	if decimalSuffix {
		return false
	}
	if _, err := strconv.ParseUint(last, 0, 64); err == nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func (v versionSnapshot) valid() bool {
	if _, err := positiveApprovalID(v.ID); err != nil {
		return false
	}
	if v.Platform != "ANDROID" && v.Platform != "IOS" {
		return false
	}
	if v.VersionName == "" || strings.TrimSpace(v.VersionName) != v.VersionName || !utf8.ValidString(v.VersionName) || utf8.RuneCountInString(v.VersionName) > 32 {
		return false
	}
	for _, r := range v.VersionName {
		if unicode.IsControl(r) {
			return false
		}
	}
	code, err := strconv.ParseInt(v.VersionCode, 10, 32)
	if err != nil || code <= 0 || strconv.FormatInt(code, 10) != v.VersionCode {
		return false
	}
	minimum, err := strconv.ParseInt(v.Minimum, 10, 32)
	if err != nil || minimum < 0 || minimum > code || strconv.FormatInt(minimum, 10) != v.Minimum {
		return false
	}
	if v.Status != "DRAFT" || v.PublishedAt != nil || v.ApprovalID != nil || v.DownloadURL == nil || !safePublicDownload(*v.DownloadURL) || !utf8.ValidString(v.Notes) || len(v.Notes) > 16000 {
		return false
	}
	if v.SHA256 == nil || !stepUpHash.MatchString(*v.SHA256) {
		return false
	}
	if v.Platform == "ANDROID" && v.CertSHA256 == nil {
		return false
	}
	if v.CertSHA256 != nil && !stepUpHash.MatchString(*v.CertSHA256) {
		return false
	}
	return true
}
func decodeVersionSnapshot(raw []byte) (versionSnapshot, error) {
	var v versionSnapshot
	if len(raw) > 24000 || decodeApprovalObject(raw, &v) != nil || !v.valid() {
		return v, httpx.NewError(httpx.ErrApprovalStale, "版本快照无效，请重新发起")
	}
	return v, nil
}
func lockVersion(ctx context.Context, tx pgx.Tx, id int64) (versionSnapshot, error) {
	var v versionSnapshot
	err := tx.QueryRow(ctx, `SELECT id::text,platform,version_name,version_code::text,min_supported_code::text,download_url,file_sha256,cert_sha256,COALESCE(release_notes,''),status,published_at::text,approval_id::text FROM app_versions WHERE id=$1 FOR UPDATE`, id).Scan(&v.ID, &v.Platform, &v.VersionName, &v.VersionCode, &v.Minimum, &v.DownloadURL, &v.SHA256, &v.CertSHA256, &v.Notes, &v.Status, &v.PublishedAt, &v.ApprovalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, httpx.NewError(httpx.ErrNotFound, "版本不存在")
	}
	return v, err
}
func (s *ApprovalService) RequestPublish(ctx context.Context, p ApprovalProof, id int64, reason string) (Approval, error) {
	e := s.auditEvent(p, publishAction, "app_version", id)
	return s.mutate(ctx, p, e, func(ctx context.Context, tx pgx.Tx, event *audit.Event) (Approval, error) {
		var out Approval
		actor, err := positiveApprovalID(p.Principal.ID)
		if err != nil {
			return out, errSession
		}
		if id <= 0 || !approvalText(reason) {
			return out, httpx.NewError(httpx.ErrBadRequest, "发布理由或编号无效")
		}
		if err = lockApprovalUsers(ctx, tx, actor, actor); err != nil {
			return out, err
		}
		if err = s.Auth.ConsumeStepUp(ctx, tx, p.Principal, p.Token, publishAction, p.RequestHash, p.IP); err != nil {
			return out, err
		}
		event.StepUpVerified = true
		if err = lockApprovalPermission(ctx, tx, actor, publishAction, 3, s.Auth.Clock.Now()); err != nil {
			return out, err
		}
		snapshot, err := lockVersion(ctx, tx, id)
		if err != nil {
			return out, err
		}
		if !snapshot.valid() {
			return out, httpx.NewError(httpx.ErrBadRequest, "必须选择资料完整且下载地址安全的草稿版本")
		}
		if err = s.freshProof(ctx, tx, p); err != nil {
			return out, err
		}
		now := s.Auth.Clock.Now()
		previous, err := scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM admin_approvals WHERE action_code=$1 AND target_type='app_version' AND target_id=$2 AND status='PENDING' ORDER BY admin_approvals.id LIMIT 1 FOR UPDATE`, publishAction, id))
		if err == nil {
			if previous.ExpiresAt.After(s.Auth.Clock.Now()) {
				if previous.RequestedBy != p.Principal.ID || previous.Reason != reason {
					return out, httpx.NewError(httpx.ErrConflict, "该草稿已有不同的待审批申请")
				}
				if previous.PayloadHash != snapshot.hash() || !bytes.Equal(canonicalSnapshot(previous.payload), snapshot.bytes()) {
					return out, httpx.NewError(httpx.ErrApprovalStale, "版本资料已变化，请处理原审批后重新发起")
				}
				out = previous
			} else {
				if _, err = tx.Exec(ctx, `UPDATE admin_approvals SET status='EXPIRED' WHERE id=$1 AND status='PENDING'`, previous.ID); err != nil {
					return out, err
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if err = s.freshProof(ctx, tx, p); err != nil {
			return out, err
		}
		if out.ID == "" {
			now = s.Auth.Clock.Now()
			out, err = scanApproval(tx.QueryRow(ctx, `INSERT INTO admin_approvals(action_code,target_type,target_id,payload,payload_hash,reason,requested_by,requested_at,expires_at) VALUES($1,'app_version',$2,$3,$4,$5,$6,$7,$8) RETURNING `+approvalColumns, publishAction, id, snapshot.bytes(), snapshot.hash(), reason, actor, now, now.Add(approvalTTL)))
			if err != nil {
				return out, err
			}
		}
		approvalID, _ := positiveApprovalID(out.ID)
		event.ApprovalID = &approvalID
		event.AfterData = approvalAuditData(out, false)
		return out, nil
	})
}
func canonicalSnapshot(raw []byte) []byte {
	v, err := decodeVersionSnapshot(raw)
	if err != nil {
		return nil
	}
	return v.bytes()
}

func (s *ApprovalService) Decide(ctx context.Context, p ApprovalProof, id int64, approve bool, note string) (Approval, error) {
	e := s.auditEvent(p, approvalDecision, "approval", id)
	if id > 0 {
		e.ApprovalID = &id
	}
	return s.mutate(ctx, p, e, func(ctx context.Context, tx pgx.Tx, event *audit.Event) (Approval, error) {
		var out Approval
		actor, err := positiveApprovalID(p.Principal.ID)
		if err != nil {
			return out, errSession
		}
		if id <= 0 || (approve && note != "") || (!approve && !approvalText(note)) {
			return out, httpx.NewError(httpx.ErrBadRequest, "审批参数无效")
		}
		// This preliminary read prevents an unprivileged caller from probing
		// approval existence. Permission rows are authoritatively locked again
		// below, after both user locks, so this does not change the lock order.
		var mayDecide bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_users u JOIN admin_user_roles ur ON ur.admin_user_id=u.id JOIN admin_role_permissions rp ON rp.role_id=ur.role_id JOIN admin_permissions p ON p.code=rp.permission_code WHERE u.id=$1 AND u.status='ACTIVE' AND p.code='approvals.decide' AND p.risk_level=2)`, actor).Scan(&mayDecide)
		if err != nil {
			return out, err
		}
		if !mayDecide {
			return out, httpx.NewError(httpx.ErrForbidden, "无权执行此操作")
		}
		var author int64
		err = tx.QueryRow(ctx, `SELECT requested_by FROM admin_approvals WHERE id=$1`, id).Scan(&author)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, httpx.NewError(httpx.ErrNotFound, "审批单不存在")
		}
		if err != nil {
			return out, err
		}
		if err = lockApprovalUsers(ctx, tx, actor, author); err != nil {
			return out, err
		}
		if err = s.Auth.ConsumeStepUp(ctx, tx, p.Principal, p.Token, approvalDecision, p.RequestHash, p.IP); err != nil {
			return out, err
		}
		event.StepUpVerified = true
		if err = lockApprovalPermission(ctx, tx, actor, approvalDecision, 2, s.Auth.Clock.Now()); err != nil {
			return out, err
		}
		out, err = scanApproval(tx.QueryRow(ctx, `SELECT `+approvalColumns+` FROM admin_approvals WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return out, err
		}
		if out.RequestedBy != strconv.FormatInt(author, 10) {
			return out, httpx.NewError(httpx.ErrApprovalStale, "审批发起人已变化")
		}
		if actor == author {
			return out, httpx.NewError(httpx.ErrApproverIsAuthor, "不能审批自己的申请")
		}
		if out.Status != "PENDING" {
			return out, httpx.NewError(httpx.ErrConflict, "审批单已处理，请刷新核实")
		}
		if !out.ExpiresAt.After(s.Auth.Clock.Now()) {
			return out, httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
		}
		event.BeforeData = approvalAuditData(out, false)
		if approve {
			executor, ok := executorFor(out.ActionCode, out.TargetType)
			if !ok {
				return out, httpx.NewError(httpx.ErrMaintenance, "该审批动作尚未接入执行器")
			}
			// 发起人与审批人双方都必须仍然持有这个动作的权限：
			// 任一方在等待期间被撤权，这张单就不该被执行。
			for _, adminID := range []int64{actor, author} {
				if err = lockApprovalPermission(ctx, tx, adminID, out.ActionCode, executor.level, s.Auth.Clock.Now()); err != nil {
					return out, err
				}
			}
			if err = executor.execute(ctx, s, tx, p, out, id, s.Auth.Clock.Now()); err != nil {
				return out, err
			}
			now := s.Auth.Clock.Now()
			out, err = scanApproval(tx.QueryRow(ctx, `UPDATE admin_approvals SET status='EXECUTED',decided_by=$2,decided_at=$3,executed_at=$3 WHERE id=$1 AND status='PENDING' RETURNING `+approvalColumns, id, actor, now))
			if err != nil {
				return out, err
			}
		} else {
			if err = s.freshProof(ctx, tx, p); err != nil {
				return out, err
			}
			now := s.Auth.Clock.Now()
			if !out.ExpiresAt.After(now) {
				return out, httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
			}
			out, err = scanApproval(tx.QueryRow(ctx, `UPDATE admin_approvals SET status='REJECTED',decided_by=$2,decided_at=$3,decision_note=$4 WHERE id=$1 AND status='PENDING' RETURNING `+approvalColumns, id, actor, now, note))
			if err != nil {
				return out, err
			}
		}
		event.AfterData = approvalAuditData(out, !approve)
		return out, nil
	})
}

// executePublish 是 app_versions.publish 的执行器。内容与重构前逐行一致：
// 审批单里的快照、当前草稿、payload_hash 三者必须完全对得上，且必须仍是 DRAFT。
func executePublish(ctx context.Context, s *ApprovalService, tx pgx.Tx, p ApprovalProof,
	out Approval, id int64, _ time.Time) error {
	if out.TargetID == nil {
		return httpx.NewError(httpx.ErrApprovalStale, "审批目标无效")
	}
	targetID, err := positiveApprovalID(*out.TargetID)
	if err != nil {
		return httpx.NewError(httpx.ErrApprovalStale, "审批目标无效")
	}
	saved, err := decodeVersionSnapshot(out.payload)
	if err != nil {
		return err
	}
	current, err := lockVersion(ctx, tx, targetID)
	if err != nil {
		return err
	}
	if !current.valid() || saved.ID != *out.TargetID || saved.hash() != out.PayloadHash || current.hash() != out.PayloadHash {
		return httpx.NewError(httpx.ErrApprovalStale, "版本资料已变化，请重新发起")
	}
	if err = s.freshProof(ctx, tx, p); err != nil {
		return err
	}
	now := s.Auth.Clock.Now()
	if !out.ExpiresAt.After(now) {
		return httpx.NewError(httpx.ErrApprovalStale, "审批单已过期，请重新发起")
	}
	tag, err := tx.Exec(ctx, `UPDATE app_versions SET status='PUBLISHED',published_at=$2,approval_id=$3 WHERE id=$1 AND status='DRAFT'`, targetID, now, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return httpx.NewError(httpx.ErrApprovalStale, "版本状态已变化")
	}
	return nil
}
