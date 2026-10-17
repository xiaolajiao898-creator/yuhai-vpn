-- +goose Up
-- =====================================================================
-- 4. 订单与充值
-- =====================================================================

CREATE TABLE receive_addresses (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  network       TEXT         NOT NULL CHECK (network IN ('TRON', 'MOCK')),
  address       VARCHAR(64)  NOT NULL UNIQUE,
  label         VARCHAR(64),
  signature     TEXT         NOT NULL,        -- 资金系统离线密钥签名
  sig_key_id    VARCHAR(32)  NOT NULL,
  valid_until   TIMESTAMPTZ  NOT NULL,
  status        TEXT         NOT NULL DEFAULT 'PAUSED' CHECK (status IN ('ACTIVE', 'PAUSED', 'RETIRED')),
  approval_id   BIGINT,                        -- 导入审批单
  created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE plan_quotes (
  id                 UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            BIGINT       NOT NULL REFERENCES users(id),
  plan_id            BIGINT       NOT NULL REFERENCES plans(id),
  plan_version       INT          NOT NULL,
  base_amount_micro  BIGINT       NOT NULL CHECK (base_amount_micro > 0 AND base_amount_micro % 100000 = 0),
  detail             JSONB        NOT NULL,     -- 折算、按天计价明细、预估新到期时间
  expires_at         TIMESTAMPTZ  NOT NULL,     -- 10 分钟
  used_by_order_id   BIGINT       UNIQUE,
  created_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE orders (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_no            VARCHAR(24)  NOT NULL UNIQUE,
  user_id             BIGINT       NOT NULL REFERENCES users(id),
  order_type          TEXT         NOT NULL CHECK (order_type IN ('PLAN', 'TOPUP')),
  plan_id             BIGINT       REFERENCES plans(id),
  quote_id            UUID         REFERENCES plan_quotes(id),
  plan_snapshot       JSONB,                    -- 下单时的套餐完整快照
  base_amount_micro   BIGINT       NOT NULL CHECK (base_amount_micro > 0),
  pay_amount_micro    BIGINT       NOT NULL,
  pay_method          TEXT         NOT NULL CHECK (pay_method IN ('USDT_TRC20', 'BALANCE')),
  receive_address_id  BIGINT       REFERENCES receive_addresses(id),
  status              TEXT         NOT NULL DEFAULT 'PENDING' CHECK (status IN (
                        'PENDING', 'CONFIRMING', 'PAID', 'COMPLETED', 'EXPIRED', 'CANCELLED')),
  fulfillment_result  TEXT         CHECK (fulfillment_result IN ('FULFILLED', 'CREDITED_TO_BALANCE')),
  chain_transfer_id   BIGINT       UNIQUE,      -- 一笔链上转账只能对应一个订单
  is_late_payment     BOOLEAN      NOT NULL DEFAULT false,
  client_idem_key     VARCHAR(64)  NOT NULL,
  expires_at          TIMESTAMPTZ  NOT NULL,
  paid_at             TIMESTAMPTZ,
  completed_at        TIMESTAMPTZ,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (user_id, client_idem_key),
  CHECK (pay_amount_micro >= base_amount_micro),
  CHECK (order_type <> 'PLAN'  OR plan_id IS NOT NULL),
  CHECK (order_type <> 'TOPUP' OR pay_method = 'USDT_TRC20'),
  CHECK (pay_method <> 'USDT_TRC20' OR receive_address_id IS NOT NULL)
);
CREATE INDEX idx_orders_match   ON orders (receive_address_id, pay_amount_micro, created_at DESC)
  WHERE pay_method = 'USDT_TRC20';
CREATE INDEX idx_orders_user    ON orders (user_id, created_at DESC);
CREATE INDEX idx_orders_pending ON orders (expires_at) WHERE status IN ('PENDING', 'CONFIRMING');

CREATE TABLE amount_locks (
  receive_address_id  BIGINT       NOT NULL REFERENCES receive_addresses(id),
  amount_micro        BIGINT       NOT NULL,
  order_no            VARCHAR(24)  NOT NULL,
  locked_until        TIMESTAMPTZ  NOT NULL,     -- 订单过期时间 + 冷却期
  PRIMARY KEY (receive_address_id, amount_micro)
);

CREATE TABLE chain_transfers (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  network             TEXT         NOT NULL CHECK (network IN ('TRON', 'MOCK')),
  tx_hash             VARCHAR(80)  NOT NULL,
  event_index         INT          NOT NULL DEFAULT 0,
  contract_address    VARCHAR(64)  NOT NULL,
  from_address        VARCHAR(64)  NOT NULL,
  to_address          VARCHAR(64)  NOT NULL,
  receive_address_id  BIGINT       NOT NULL REFERENCES receive_addresses(id),
  amount_micro        BIGINT       NOT NULL CHECK (amount_micro > 0),
  block_number        BIGINT       NOT NULL,
  block_time          TIMESTAMPTZ  NOT NULL,
  confirm_status      TEXT         NOT NULL CHECK (confirm_status IN ('SEEN', 'SOLIDIFIED', 'FAILED')),
  match_status        TEXT         NOT NULL DEFAULT 'UNPROCESSED'
                      CHECK (match_status IN ('UNPROCESSED', 'MATCHED', 'EXCEPTION')),
  order_id            BIGINT       UNIQUE REFERENCES orders(id),
  data_source         VARCHAR(32)  NOT NULL,     -- TRONGRID / FULLNODE / MOCK
  raw                 JSONB,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
  processed_at        TIMESTAMPTZ,
  UNIQUE (network, tx_hash, event_index)
);
CREATE INDEX idx_chain_transfers_todo ON chain_transfers (created_at)
  WHERE match_status = 'UNPROCESSED' AND confirm_status = 'SOLIDIFIED';
CREATE INDEX idx_chain_transfers_from ON chain_transfers (from_address);
CREATE TRIGGER trg_chain_transfers_no_delete BEFORE DELETE ON chain_transfers
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

ALTER TABLE orders ADD CONSTRAINT fk_orders_chain_transfer
  FOREIGN KEY (chain_transfer_id) REFERENCES chain_transfers(id);

CREATE TABLE chain_scan_cursors (
  receive_address_id  BIGINT       PRIMARY KEY REFERENCES receive_addresses(id),
  last_block_time     TIMESTAMPTZ  NOT NULL,
  last_block_number   BIGINT,
  last_success_at     TIMESTAMPTZ,
  updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE deposit_exceptions (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  chain_transfer_id   BIGINT       NOT NULL UNIQUE REFERENCES chain_transfers(id),
  reason              TEXT         NOT NULL CHECK (reason IN (
                        'AMOUNT_MISMATCH', 'NO_MATCHING_ORDER', 'LATE_BEYOND_COOLDOWN',
                        'DUPLICATE_PAYMENT', 'BEFORE_ORDER_CREATED')),
  suggested_order_id  BIGINT       REFERENCES orders(id),
  status              TEXT         NOT NULL DEFAULT 'OPEN' CHECK (status IN (
                        'OPEN', 'RESOLVED_TO_ORDER', 'RESOLVED_TO_BALANCE', 'REJECTED')),
  resolution_note     VARCHAR(500),
  approval_id         BIGINT,
  resolved_at         TIMESTAMPTZ,
  created_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_deposit_exceptions_open ON deposit_exceptions (created_at) WHERE status = 'OPEN';

-- +goose Down
-- +goose StatementBegin
-- 不提供自动回滚：账务表回滚需人工评审后执行
SELECT 1;
-- +goose StatementEnd
