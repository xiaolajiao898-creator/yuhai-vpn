-- +goose Up
ALTER TABLE admin_approvals ADD COLUMN decision_note VARCHAR(500);
CREATE INDEX idx_admin_approvals_requester ON admin_approvals(requested_by,requested_at DESC,id DESC);
-- Fail migration on legacy conflicting pending publications; never silently
-- pick or delete a real approval on an operator's behalf.
CREATE UNIQUE INDEX idx_admin_pending_version_publish ON admin_approvals(target_id)
 WHERE action_code='app_versions.publish' AND target_type='app_version' AND status='PENDING';

-- Define capabilities only. No role or administrator receives these grants.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('approvals.read','查看审批',0), ('approvals.decide','审批决定',2),
 ('app_versions.publish','申请和审批APP版本发布',3), ('audit.read','查看审计',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
SELECT 1;
