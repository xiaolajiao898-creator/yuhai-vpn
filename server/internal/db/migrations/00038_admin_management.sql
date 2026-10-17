-- +goose Up
-- Define capabilities without granting them to any administrator or role.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('admins.read','查看管理员与角色',0),
 ('admins.create','申请与审批新建管理员',3),
 ('admins.roles','申请与审批管理员角色变更',3)
 ON CONFLICT(code) DO NOTHING;

-- Idempotency points to the approval; secrets never enter its public payload.
-- credentials_enc contains the password hash and TOTP secret encrypted using
-- this operation UUID as AAD. It is cleared in the account-creation transaction.
CREATE TABLE admin_management_requests (
 operation_id UUID PRIMARY KEY,
 admin_user_id BIGINT NOT NULL REFERENCES admin_users(id),
 session_id BIGINT NOT NULL REFERENCES admin_sessions(id),
 action_code TEXT NOT NULL CHECK(action_code IN ('admins.create','admins.roles')),
 request_hash CHAR(64) NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),
 proof_hash CHAR(64) NOT NULL CHECK(proof_hash ~ '^[a-f0-9]{64}$'),
 approval_id BIGINT NOT NULL UNIQUE REFERENCES admin_approvals(id),
 credentials_enc BYTEA,
 created_at TIMESTAMPTZ NOT NULL
);
REVOKE ALL ON admin_management_requests FROM yuhai_app,yuhai_readonly;
GRANT SELECT,INSERT,UPDATE ON admin_management_requests TO yuhai_admin;
REVOKE DELETE,TRUNCATE ON admin_management_requests FROM yuhai_admin;
-- Existing service grants already allow inserts; role replacement additionally
-- needs DELETE on this mapping only, never on users or audit history.
GRANT SELECT,INSERT,UPDATE ON admin_users TO yuhai_admin;
GRANT SELECT,INSERT,DELETE ON admin_user_roles TO yuhai_admin;
-- Row locking of role definitions freezes FK-backed permission additions.
GRANT SELECT,UPDATE ON admin_roles TO yuhai_admin;

-- Provisioning secrets are wiped whenever the approval enters a terminal state.
-- Unprocessed expired approvals retain only encrypted, unusable credentials.
-- +goose StatementBegin
CREATE FUNCTION clear_admin_provisioning_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action_code='admins.create' AND NEW.status IN ('EXECUTED','REJECTED','EXPIRED','FAILED') THEN
  UPDATE admin_management_requests SET credentials_enc=NULL WHERE approval_id=NEW.id;
 END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER trg_clear_admin_provisioning_credentials
 AFTER UPDATE OF status ON admin_approvals FOR EACH ROW
 EXECUTE FUNCTION clear_admin_provisioning_credentials();

-- +goose Down
-- Accounts and approval history require deliberate operator-managed rollback.
SELECT 1;
