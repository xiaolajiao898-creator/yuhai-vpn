-- +goose Up
-- 资金地址的两个写操作：收款地址启停、共享付款地址登记。
--
-- 两张表此前都只能靠手写 SQL 维护：
--   receive_addresses    —— 状态决定 internal/order 的 pickAddress 会不会把它分给新订单；
--   shared_pay_addresses —— 唯一读取方是 internal/invite/risk_rewards.go，命中它的付款地址
--                           不会被当成自邀请的铁证，而是转人工复核。
--
-- 两个能力分开定义：改收款通道的可用性和标注交易所热钱包，是两件不相干的事，
-- 应该可以分给不同的人。均为 L2 + 二次验证，只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('finance.address_status','启用或停用收款地址',2),
 ('finance.shared_pay_write','登记公共付款地址',2)
 ON CONFLICT(code) DO NOTHING;

-- 资金地址写的幂等记录。形状与 admin_risk_mutations 等相同；分表理由也相同。
CREATE TABLE admin_finance_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('finance.address_status','finance.shared_pay_write')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_finance_mutations_admin_idx ON admin_finance_mutations (admin_user_id, created_at);

-- shared_pay_addresses 的主键是地址本身，没有数字编号，而上面那套骨架按数字编号记 target。
-- 加一个只用于幂等记账的自增编号：它不参与任何业务判断，也不改变主键。
ALTER TABLE shared_pay_addresses ADD COLUMN id BIGINT GENERATED ALWAYS AS IDENTITY;
CREATE UNIQUE INDEX shared_pay_addresses_id_idx ON shared_pay_addresses (id);

-- +goose Down
DROP INDEX shared_pay_addresses_id_idx;
ALTER TABLE shared_pay_addresses DROP COLUMN id;
DROP TABLE admin_finance_mutations;
DELETE FROM admin_permissions WHERE code IN ('finance.address_status','finance.shared_pay_write');
