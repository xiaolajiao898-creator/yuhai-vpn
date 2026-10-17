-- +goose Up
-- =====================================================================
-- 3. 余额
-- =====================================================================

CREATE TABLE user_balances (
  user_id          BIGINT       PRIMARY KEY REFERENCES users(id),
  available_micro  BIGINT       NOT NULL DEFAULT 0 CHECK (available_micro >= 0),
  version          BIGINT       NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE balance_ledger (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id              BIGINT       NOT NULL REFERENCES users(id),
  entry_type           TEXT         NOT NULL CHECK (entry_type IN (
                         'DEPOSIT', 'PURCHASE', 'INVITE_REWARD', 'EXCEPTION_CREDIT',
                         'FULFILL_FALLBACK', 'ADMIN_CREDIT', 'ADMIN_DEBIT')),
  amount_micro         BIGINT       NOT NULL CHECK (amount_micro <> 0),   -- 正数入账，负数出账
  balance_after_micro  BIGINT       NOT NULL CHECK (balance_after_micro >= 0),
  source_type          TEXT         NOT NULL CHECK (source_type IN (
                         'CHAIN_TRANSFER', 'ORDER', 'INVITE_REWARD', 'DEPOSIT_EXCEPTION', 'ADMIN_APPROVAL')),
  source_id            BIGINT       NOT NULL,
  idempotency_key      VARCHAR(128) NOT NULL UNIQUE,
  operator_type        TEXT         NOT NULL CHECK (operator_type IN ('USER', 'ADMIN', 'SYSTEM')),
  operator_id          BIGINT,
  remark               VARCHAR(255),
  created_at           TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_balance_ledger_user ON balance_ledger (user_id, created_at DESC);
CREATE TRIGGER trg_balance_ledger_append_only BEFORE UPDATE OR DELETE ON balance_ledger
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
