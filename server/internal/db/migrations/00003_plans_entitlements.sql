-- +goose Up
-- =====================================================================
-- 2. 套餐与权益
-- =====================================================================

CREATE TABLE plans (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code                VARCHAR(32)  NOT NULL UNIQUE,
  name                VARCHAR(64)  NOT NULL,
  plan_type           TEXT         NOT NULL CHECK (plan_type IN ('TIME', 'TRAFFIC', 'DEVICE_ADDON')),
  tier                SMALLINT     NOT NULL DEFAULT 1 CHECK (tier BETWEEN 1 AND 9),
  duration_days       INT,                                    -- TIME
  period_quota_bytes  BIGINT,                                 -- TIME：每 30 天配额，NULL 表示不限
  pack_bytes          BIGINT,                                 -- TRAFFIC
  validity_days       INT,                                    -- TRAFFIC
  device_limit        INT,                                    -- TIME / TRAFFIC
  extra_devices       INT,                                    -- DEVICE_ADDON
  price_micro         BIGINT       NOT NULL
                      CHECK (price_micro > 0 AND price_micro % 100000 = 0),   -- 0.1 USDT 精度
  display_config      JSONB        NOT NULL DEFAULT '{}',     -- 角标、描述、分组
  sort_order          INT          NOT NULL DEFAULT 0,
  status              TEXT         NOT NULL DEFAULT 'DRAFT'
                      CHECK (status IN ('DRAFT', 'ON_SALE', 'OFF_SALE')),
  version             INT          NOT NULL DEFAULT 1,        -- 改价或改配置时 +1
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  CONSTRAINT chk_plan_shape CHECK (
       (plan_type = 'TIME'         AND duration_days > 0 AND device_limit > 0)
    OR (plan_type = 'TRAFFIC'      AND pack_bytes > 0 AND validity_days > 0 AND device_limit > 0)
    OR (plan_type = 'DEVICE_ADDON' AND extra_devices > 0)
  )
);

CREATE TABLE subscriptions (
  user_id             BIGINT       PRIMARY KEY REFERENCES users(id),
  plan_id             BIGINT       REFERENCES plans(id),
  tier                SMALLINT     NOT NULL DEFAULT 0,        -- 0 表示没有有效时长套餐
  started_at          TIMESTAMPTZ,
  expire_at           TIMESTAMPTZ,
  device_limit        INT          NOT NULL DEFAULT 0,
  period_quota_bytes  BIGINT,
  period_used_bytes   BIGINT       NOT NULL DEFAULT 0 CHECK (period_used_bytes >= 0),
  period_reset_at     TIMESTAMPTZ,
  version             BIGINT       NOT NULL DEFAULT 0,
  updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscriptions_expire ON subscriptions (expire_at) WHERE tier > 0;

CREATE TABLE traffic_packs (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id       BIGINT       NOT NULL REFERENCES users(id),
  tier          SMALLINT     NOT NULL,
  device_limit  INT          NOT NULL,
  total_bytes   BIGINT       NOT NULL CHECK (total_bytes > 0),
  used_bytes    BIGINT       NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
  expire_at     TIMESTAMPTZ  NOT NULL,
  source_type   TEXT         NOT NULL CHECK (source_type IN ('ORDER', 'INVITE_REWARD', 'ADMIN')),
  source_id     BIGINT       NOT NULL,
  status        TEXT         NOT NULL DEFAULT 'ACTIVE'
                CHECK (status IN ('ACTIVE', 'EXHAUSTED', 'EXPIRED')),
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id)
);
CREATE INDEX idx_traffic_packs_user_active ON traffic_packs (user_id, expire_at) WHERE status = 'ACTIVE';

CREATE TABLE device_addons (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      BIGINT       NOT NULL REFERENCES users(id),
  quantity     INT          NOT NULL CHECK (quantity > 0),
  expire_at    TIMESTAMPTZ  NOT NULL,
  source_type  TEXT         NOT NULL CHECK (source_type IN ('ORDER', 'ADMIN')),
  source_id    BIGINT       NOT NULL,
  created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (source_type, source_id)
);
CREATE INDEX idx_device_addons_user ON device_addons (user_id, expire_at);

CREATE TABLE subscription_ledger (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id              BIGINT       NOT NULL REFERENCES users(id),
  change_type          TEXT         NOT NULL CHECK (change_type IN (
                         'PURCHASE', 'RENEW', 'TIER_CHANGE', 'TRAFFIC_PACK', 'DEVICE_ADDON',
                         'INVITE_REWARD', 'ADMIN_ADJUST', 'TRAFFIC_CONSUME_OVERFLOW',
                         'PERIOD_RESET', 'EXPIRE', 'REVOKE')),
  source_type          TEXT         NOT NULL CHECK (source_type IN ('ORDER', 'INVITE_REWARD', 'ADMIN_APPROVAL', 'SYSTEM')),
  source_id            BIGINT,
  delta_seconds        BIGINT       NOT NULL DEFAULT 0,
  delta_bytes          BIGINT       NOT NULL DEFAULT 0,
  delta_devices        INT          NOT NULL DEFAULT 0,
  before_snapshot      JSONB        NOT NULL,
  after_snapshot       JSONB        NOT NULL,
  idempotency_key      VARCHAR(128) NOT NULL UNIQUE,
  operator_type        TEXT         NOT NULL CHECK (operator_type IN ('USER', 'ADMIN', 'SYSTEM')),
  operator_id          BIGINT,
  remark               VARCHAR(255),
  created_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscription_ledger_user ON subscription_ledger (user_id, created_at DESC);
CREATE TRIGGER trg_subscription_ledger_append_only BEFORE UPDATE OR DELETE ON subscription_ledger
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
