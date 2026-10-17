-- +goose Up
-- App/worker credentials must not mint administrator sessions, replace MFA
-- material or rewrite role grants. Export jobs keep their separate grants.
REVOKE ALL ON admin_users, admin_roles, admin_permissions, admin_role_permissions,
 admin_user_roles, admin_sessions, admin_step_up_tokens, admin_mfa_tickets,
 admin_login_limits, admin_approvals, admin_management_requests,
 admin_version_mutations, admin_device_mutations, admin_node_mutations,
 admin_node_security_mutations, admin_risk_mutations, admin_invite_mutations,
 admin_plan_mutations, admin_config_mutations, admin_finance_mutations,
 admin_export_mutations
 FROM yuhai_app, PUBLIC;
REVOKE ALL ON admin_users, admin_sessions, admin_step_up_tokens, admin_mfa_tickets,
 admin_login_limits, admin_management_requests, admin_approvals
 FROM yuhai_readonly;
-- Triggers protect UPDATE/DELETE rows, but TRUNCATE bypasses row triggers.
-- Both business accounts therefore need privilege-level append-only guards.
REVOKE UPDATE, DELETE, TRUNCATE ON balance_ledger, subscription_ledger,
 device_bind_logs, admin_audit_logs FROM yuhai_app, yuhai_admin, PUBLIC;
REVOKE DELETE, TRUNCATE ON orders, chain_transfers FROM yuhai_app, yuhai_admin, PUBLIC;

-- +goose Down
-- Do not automatically restore privilege escalation or mutable history.
SELECT 1;
