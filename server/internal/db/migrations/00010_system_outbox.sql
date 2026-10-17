-- +goose Up
-- =====================================================================
-- 9. 系统
-- =====================================================================

CREATE TABLE system_configs (
  key         VARCHAR(64)  PRIMARY KEY,       -- order.ttl_minutes / amount.cooldown_minutes / invite.freeze_hours ...
  value       JSONB        NOT NULL,
  version     INT          NOT NULL DEFAULT 1,
  updated_by  BIGINT,
  updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE app_versions (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  platform            TEXT         NOT NULL CHECK (platform IN ('ANDROID', 'IOS')),
  version_name        VARCHAR(32)  NOT NULL,
  version_code        INT          NOT NULL,
  min_supported_code  INT          NOT NULL,
  download_url        VARCHAR(512),
  file_sha256         CHAR(64),
  cert_sha256         CHAR(64),                -- Android 签名证书指纹
  release_notes       TEXT,
  status              TEXT         NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'PUBLISHED', 'WITHDRAWN')),
  approval_id         BIGINT,
  published_at        TIMESTAMPTZ,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (platform, version_code)
);

CREATE TABLE outbox_events (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_type      VARCHAR(64)  NOT NULL,
  aggregate_type  VARCHAR(32)  NOT NULL,
  aggregate_id    BIGINT       NOT NULL,
  payload         JSONB        NOT NULL,
  status          TEXT         NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'DONE', 'DEAD')),
  attempts        INT          NOT NULL DEFAULT 0,
  next_retry_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  last_error      TEXT,
  created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (event_type, aggregate_type, aggregate_id)
);
CREATE INDEX idx_outbox_pending ON outbox_events (next_retry_at) WHERE status = 'PENDING';

CREATE TABLE api_idempotency (
  user_id       BIGINT       NOT NULL,
  idem_key      VARCHAR(64)  NOT NULL,
  endpoint      VARCHAR(64)  NOT NULL,
  request_hash  CHAR(64)     NOT NULL,
  status_code   INT,
  response      JSONB,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),   -- 保留 24 小时
  PRIMARY KEY (user_id, idem_key)
);

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
