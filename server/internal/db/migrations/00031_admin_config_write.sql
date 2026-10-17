-- +goose Up
-- 系统参数修改（PATCH /admin/v1/configs/{key}）。
--
-- system_configs 这张表此前**没有任何接口能写**：改一个下单金额下限要连库执行 UPDATE。
-- 而它被三处业务实时读取（订单冷却、充值上下限、邀请奖励套餐编号），
-- 手改容易写出「代码根本不读的键」或「读了也用不了的值」，两者都不会报错、只会静默失效。
--
-- 能力单列，L2 + 二次验证：改的是全站生效的业务参数，不该随只读权限一起给出去。
-- 只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('configs.write','修改系统参数',2)
 ON CONFLICT(code) DO NOTHING;

-- 配置写的幂等记录。形状与 admin_risk_mutations / admin_invite_mutations / admin_plan_mutations 相同，
-- 分表的理由也相同：target 不是同一类东西，混在一张表里会让「这个 operation_id 用在哪个对象上」
-- 需要额外解释。
--
-- 注意 target_id：配置的目标是字符串 key，而这套骨架的幂等表按数字编号记 target。
-- 因此白名单里给每个 key 配了一个固定编号（见 internal/admin/config_write.go 的 configKeys），
-- 真正的 key 记在响应与审计数据里。
CREATE TABLE admin_config_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code = 'configs.write'),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_config_mutations_admin_idx ON admin_config_mutations (admin_user_id, created_at);

-- +goose Down
DROP TABLE admin_config_mutations;
DELETE FROM admin_permissions WHERE code='configs.write';
