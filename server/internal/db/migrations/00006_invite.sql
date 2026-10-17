-- +goose Up
-- =====================================================================
-- 5. 邀请
-- =====================================================================

CREATE TABLE invite_relations (
  invitee_user_id  BIGINT       PRIMARY KEY REFERENCES users(id),   -- 每个用户只能被邀请一次
  inviter_user_id  BIGINT       NOT NULL REFERENCES users(id),
  invite_code      VARCHAR(12)  NOT NULL,
  bind_method      TEXT         NOT NULL CHECK (bind_method IN ('APK_CHANNEL', 'CLIPBOARD', 'MANUAL')),
  risk_flags       JSONB        NOT NULL DEFAULT '[]',
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
  CHECK (invitee_user_id <> inviter_user_id)
);
CREATE INDEX idx_invite_relations_inviter ON invite_relations (inviter_user_id, created_at DESC);

CREATE TABLE invite_reward_rules (
  id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name                    VARCHAR(64)  NOT NULL,
  trigger_event           TEXT         NOT NULL CHECK (trigger_event IN (
                            'INVITEE_REGISTERED', 'INVITEE_FIRST_PAID', 'INVITEE_PAID')),
  beneficiary             TEXT         NOT NULL CHECK (beneficiary IN ('INVITER', 'INVITEE')),
  reward_type             TEXT         NOT NULL CHECK (reward_type IN ('DAYS', 'TRAFFIC', 'BALANCE')),
  reward_value            BIGINT       NOT NULL CHECK (reward_value >= 0),  -- 天 / 字节 / micro-USDT
  reward_percent_bp       INT          CHECK (reward_percent_bp BETWEEN 0 AND 10000),  -- 按订单金额比例（基点）
  min_order_amount_micro  BIGINT,
  freeze_hours            INT          NOT NULL DEFAULT 72,
  daily_cap_per_inviter   INT,
  status                  TEXT         NOT NULL DEFAULT 'DISABLED' CHECK (status IN ('ENABLED', 'DISABLED')),
  starts_at               TIMESTAMPTZ,
  ends_at                 TIMESTAMPTZ,
  version                 INT          NOT NULL DEFAULT 1,
  created_at              TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE invite_rewards (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  rule_id              BIGINT       NOT NULL REFERENCES invite_reward_rules(id),
  rule_snapshot        JSONB        NOT NULL,
  inviter_user_id      BIGINT       NOT NULL REFERENCES users(id),
  invitee_user_id      BIGINT       NOT NULL REFERENCES users(id),
  beneficiary_user_id  BIGINT       NOT NULL REFERENCES users(id),
  trigger_ref          VARCHAR(64)  NOT NULL,     -- 例如 first_paid 或 order:123
  reward_type          TEXT         NOT NULL CHECK (reward_type IN ('DAYS', 'TRAFFIC', 'BALANCE')),
  reward_value         BIGINT       NOT NULL,
  status               TEXT         NOT NULL CHECK (status IN ('PENDING', 'REVIEW', 'RELEASED', 'CANCELLED')),
  risk_flags           JSONB        NOT NULL DEFAULT '[]',
  release_at           TIMESTAMPTZ  NOT NULL,
  cancel_reason        VARCHAR(255),
  released_at          TIMESTAMPTZ,
  created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (rule_id, beneficiary_user_id, invitee_user_id, trigger_ref)
);
CREATE INDEX idx_invite_rewards_release ON invite_rewards (release_at) WHERE status = 'PENDING';
CREATE INDEX idx_invite_rewards_beneficiary ON invite_rewards (beneficiary_user_id, created_at DESC);

CREATE TABLE shared_pay_addresses (           -- 交易所热钱包等公共付款地址
  address        VARCHAR(64)  PRIMARY KEY,
  note           VARCHAR(128),
  auto_detected  BOOLEAN      NOT NULL DEFAULT true,
  created_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
