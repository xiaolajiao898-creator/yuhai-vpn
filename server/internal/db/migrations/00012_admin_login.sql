-- +goose Up
-- Password challenge tickets are single-use and never stored in plaintext.
CREATE TABLE admin_mfa_tickets (
 token_hash CHAR(64) PRIMARY KEY,
 admin_user_id BIGINT NOT NULL REFERENCES admin_users(id),
 ip INET NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 used_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_admin_mfa_tickets_expiry ON admin_mfa_tickets(expires_at);

-- Shared across admin-api instances; incremented before expensive password work.
CREATE TABLE admin_login_limits (
 ip_hash CHAR(64) PRIMARY KEY,
 window_start TIMESTAMPTZ NOT NULL,
 attempts INT NOT NULL CHECK(attempts > 0),
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_admin_login_limits_expiry ON admin_login_limits(expires_at);
CREATE INDEX idx_admin_sessions_expiry ON admin_sessions(expires_at);

-- +goose Down
-- Keep authentication records for deliberate operator-managed rollback.
SELECT 1;
