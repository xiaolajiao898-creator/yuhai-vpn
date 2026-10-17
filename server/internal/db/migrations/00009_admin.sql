-- +goose Up
-- =====================================================================
-- 8. 管理后台
-- =====================================================================

CREATE TABLE admin_users (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username         VARCHAR(32)  NOT NULL UNIQUE,
  password_hash    TEXT         NOT NULL,           -- Argon2id
  totp_secret_enc  BYTEA        NOT NULL,
  totp_last_step   BIGINT       NOT NULL DEFAULT 0, -- 防止同一 TOTP 码重复使用
  status           TEXT         NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'LOCKED', 'DISABLED')),
  failed_attempts  INT          NOT NULL DEFAULT 0,
  locked_until     TIMESTAMPTZ,
  last_login_at    TIMESTAMPTZ,
  last_login_ip    INET,
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE admin_roles (
  id    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code  VARCHAR(32)  NOT NULL UNIQUE,
  name  VARCHAR(64)  NOT NULL
);

CREATE TABLE admin_permissions (
  code        VARCHAR(64)  PRIMARY KEY,           -- 例如 user.balance.adjust
  name        VARCHAR(64)  NOT NULL,
  risk_level  SMALLINT     NOT NULL CHECK (risk_level BETWEEN 0 AND 3)   -- L0–L3
);

CREATE TABLE admin_role_permissions (
  role_id          BIGINT       NOT NULL REFERENCES admin_roles(id),
  permission_code  VARCHAR(64)  NOT NULL REFERENCES admin_permissions(code),
  PRIMARY KEY (role_id, permission_code)
);

CREATE TABLE admin_user_roles (
  admin_user_id  BIGINT  NOT NULL REFERENCES admin_users(id),
  role_id        BIGINT  NOT NULL REFERENCES admin_roles(id),
  PRIMARY KEY (admin_user_id, role_id)
);

CREATE TABLE admin_sessions (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  admin_user_id   BIGINT       NOT NULL REFERENCES admin_users(id),
  token_hash      CHAR(64)     NOT NULL UNIQUE,
  ip              INET         NOT NULL,
  user_agent      VARCHAR(255),
  last_active_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
  expires_at      TIMESTAMPTZ  NOT NULL,
  revoked_at      TIMESTAMPTZ,
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE admin_step_up_tokens (
  token_hash    CHAR(64)     PRIMARY KEY,
  session_id    BIGINT       NOT NULL REFERENCES admin_sessions(id),
  action_code   VARCHAR(64)  NOT NULL,
  payload_hash  CHAR(64)     NOT NULL,        -- 与具体请求体绑定
  expires_at    TIMESTAMPTZ  NOT NULL,        -- 5 分钟
  used_at       TIMESTAMPTZ
);

CREATE TABLE admin_approvals (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  action_code   VARCHAR(64)  NOT NULL,
  target_type   VARCHAR(32)  NOT NULL,
  target_id     BIGINT,
  payload       JSONB        NOT NULL,
  payload_hash  CHAR(64)     NOT NULL,
  reason        VARCHAR(500) NOT NULL,
  requested_by  BIGINT       NOT NULL REFERENCES admin_users(id),
  requested_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
  status        TEXT         NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                  'PENDING', 'APPROVED', 'REJECTED', 'EXECUTED', 'EXPIRED', 'FAILED')),
  decided_by    BIGINT       REFERENCES admin_users(id),
  decided_at    TIMESTAMPTZ,
  executed_at   TIMESTAMPTZ,
  expires_at    TIMESTAMPTZ  NOT NULL,        -- 24 小时未处理自动过期
  CHECK (decided_by IS NULL OR decided_by <> requested_by)
);
CREATE INDEX idx_admin_approvals_pending ON admin_approvals (requested_at) WHERE status = 'PENDING';

CREATE TABLE admin_audit_logs (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  admin_user_id     BIGINT,
  action_code       VARCHAR(64)  NOT NULL,
  target_type       VARCHAR(32),
  target_id         BIGINT,
  request_id        VARCHAR(32)  NOT NULL,
  ip                INET,
  user_agent        VARCHAR(255),
  before_data       JSONB,
  after_data        JSONB,
  result            TEXT         NOT NULL CHECK (result IN ('SUCCESS', 'FAILED', 'DENIED')),
  step_up_verified  BOOLEAN      NOT NULL DEFAULT false,
  approval_id       BIGINT,
  prev_hash         CHAR(64)     NOT NULL,
  row_hash          CHAR(64)     NOT NULL,    -- SHA-256(prev_hash || 规范化 JSON(本行其余字段))
  created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_admin_audit_logs_admin  ON admin_audit_logs (admin_user_id, created_at DESC);
CREATE INDEX idx_admin_audit_logs_target ON admin_audit_logs (target_type, target_id);
CREATE TRIGGER trg_admin_audit_logs_append_only BEFORE UPDATE OR DELETE ON admin_audit_logs
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
