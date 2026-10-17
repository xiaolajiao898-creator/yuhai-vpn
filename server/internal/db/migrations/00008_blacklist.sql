-- +goose Up
-- =====================================================================
-- 7. 黑名单
-- =====================================================================

CREATE TABLE blacklist (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  target_type   TEXT         NOT NULL CHECK (target_type IN (
                  'USER', 'DEVICE', 'FINGERPRINT', 'PAY_ADDRESS', 'IP_PREFIX', 'INVITE_CODE')),
  target_value  VARCHAR(128) NOT NULL,      -- 指纹与 IP 前缀存哈希
  effects       TEXT[]       NOT NULL,      -- 例如 {LOGIN,CONNECT,ORDER,INVITE_REWARD}
  reason        VARCHAR(255) NOT NULL,
  status        TEXT         NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'REMOVED')),
  expires_at    TIMESTAMPTZ,
  created_by    BIGINT       NOT NULL,
  removed_by    BIGINT,
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  removed_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX uq_blacklist_active ON blacklist (target_type, target_value) WHERE status = 'ACTIVE';

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
