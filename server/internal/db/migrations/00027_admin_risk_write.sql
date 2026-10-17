-- +goose Up
-- 风控处置：黑名单增删查、账户状态变更。
--
-- 三个能力分开定义，权限码与二次验证的 action_code 一一对应：
--   risk.read             只读黑名单（L0）
--   risk.blacklist_add    新增黑名单（L2，需二次验证）
--   risk.blacklist_remove 解除黑名单（L2，需二次验证）
--   users.status          停用/封禁/恢复账户（L2，需二次验证）
--
-- 新增与解除分开：解除是把人放回来，和把人拦住不是同一种权力，应当可以分给不同的人。
-- 权限码与二次验证的 action_code 一一对应（与 nodes.enroll_token / nodes.revoke 同一约定）。
-- 只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('risk.read','查看黑名单',0),
 ('risk.blacklist_add','新增黑名单条目',2),
 ('risk.blacklist_remove','解除黑名单条目',2),
 ('users.status','停用、封禁或恢复账户',2)
 ON CONFLICT(code) DO NOTHING;

-- 风控 L2 操作的幂等记录。与 admin_node_security_mutations 是同一套形状但分表：
-- 两者的 target 不同（一个是 node，一个是 blacklist/user），混在一张表里
-- 会让「这个 operation_id 用在哪个对象上」需要额外解释。
CREATE TABLE admin_risk_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('risk.blacklist_add','risk.blacklist_remove','users.status')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_risk_mutations_admin_idx ON admin_risk_mutations (admin_user_id, created_at);

-- 同一目标同时只允许一条生效的黑名单：否则解除一条之后另一条还在，
-- 运维会以为已经放行。已解除（REMOVED）的历史条目不受此约束，留痕照常。
CREATE UNIQUE INDEX blacklist_active_target_idx
  ON blacklist (target_type, target_value) WHERE status='ACTIVE';

-- +goose Down
DROP INDEX blacklist_active_target_idx;
DROP TABLE admin_risk_mutations;
DELETE FROM admin_permissions WHERE code IN ('risk.read','risk.blacklist_add','risk.blacklist_remove','users.status');
