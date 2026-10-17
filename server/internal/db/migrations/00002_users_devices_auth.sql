-- +goose Up
-- =====================================================================
-- 1. 用户、设备、认证
-- =====================================================================

CREATE TABLE users (
  id                       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  account_no               VARCHAR(10)  NOT NULL UNIQUE,
  secret_lookup            CHAR(64)     NOT NULL UNIQUE,     -- HMAC-SHA256(pepper, 账户密钥)
  secret_version           INT          NOT NULL DEFAULT 1,
  invite_code              VARCHAR(12)  NOT NULL UNIQUE,
  status                   TEXT         NOT NULL DEFAULT 'ACTIVE'
                           CHECK (status IN ('ACTIVE', 'SUSPENDED', 'BANNED')),
  risk_flags               JSONB        NOT NULL DEFAULT '[]',
  register_channel         VARCHAR(32),
  register_ip_hash         CHAR(64),                          -- 30 天后由任务置空
  register_ip_prefix_hash  CHAR(64),                          -- /24 前缀哈希，30 天后置空
  created_at               TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE devices (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id           BIGINT       NOT NULL REFERENCES users(id),
  device_uid        UUID         NOT NULL UNIQUE,
  pubkey            TEXT         NOT NULL,                    -- P-256 公钥（SPKI Base64）
  platform          TEXT         NOT NULL CHECK (platform IN ('ANDROID', 'IOS')),
  model             VARCHAR(64),
  os_version        VARCHAR(32),
  app_version       VARCHAR(32),
  fingerprint_hash  CHAR(64),                                 -- 仅用于风控比对
  display_name      VARCHAR(64),
  status            TEXT         NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REVOKED')),
  last_seen_at      TIMESTAMPTZ,
  created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
  revoked_at        TIMESTAMPTZ
);
CREATE INDEX idx_devices_user_active ON devices (user_id) WHERE status = 'ACTIVE';
CREATE INDEX idx_devices_fingerprint ON devices (fingerprint_hash);

CREATE TABLE device_credentials (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  device_id         BIGINT       NOT NULL UNIQUE REFERENCES devices(id),
  proxy_uuid        UUID         NOT NULL UNIQUE,             -- VLESS
  proxy_secret_enc  BYTEA        NOT NULL,                    -- Trojan / SS-2022 口令（加密）
  wg_pubkey         VARCHAR(64)  UNIQUE,                      -- 设备上传；私钥不离开设备
  wg_address        INET         UNIQUE,                      -- 隧道内地址，全局分配
  status            TEXT         NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REVOKED')),
  rotated_at        TIMESTAMPTZ,
  created_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE device_bind_logs (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id          BIGINT       NOT NULL,
  device_id        BIGINT       NOT NULL,
  action           TEXT         NOT NULL CHECK (action IN (
                     'REGISTER', 'RESTORE', 'REVOKE_BY_USER', 'REVOKE_BY_ADMIN',
                     'REVOKE_BY_SECRET_RESET', 'CREDENTIAL_ROTATE')),
  operator_type    TEXT         NOT NULL CHECK (operator_type IN ('USER', 'ADMIN', 'SYSTEM')),
  operator_id      BIGINT,
  ip_hash          CHAR(64),
  detail           JSONB        NOT NULL DEFAULT '{}',
  idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_device_bind_logs_user ON device_bind_logs (user_id, created_at DESC);
CREATE TRIGGER trg_device_bind_logs_append_only BEFORE UPDATE OR DELETE ON device_bind_logs
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE auth_sessions (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id         BIGINT       NOT NULL REFERENCES users(id),
  device_id       BIGINT       NOT NULL REFERENCES devices(id),
  refresh_hash    CHAR(64)     NOT NULL UNIQUE,               -- SHA-256(refresh_token)
  family_id       UUID         NOT NULL,                      -- 同一登录的令牌族；检测到旧令牌重放则整族作废
  secret_version  INT          NOT NULL,
  expires_at      TIMESTAMPTZ  NOT NULL,
  rotated_at      TIMESTAMPTZ,
  revoked_at      TIMESTAMPTZ,
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_auth_sessions_family ON auth_sessions (family_id);

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
