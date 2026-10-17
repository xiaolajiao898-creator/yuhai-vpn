-- +goose Up
-- 套餐管理：列表、新建、修改、上下架。
--
-- 在这之前套餐只能靠手写 SQL 改库，而套餐是全系统唯一一个**既决定用户买到什么、
-- 又决定收多少钱**的对象。
--
--   plans.read    查看全部套餐，含草稿与已下架（L0）
--   plans.create  新建套餐（L2，需二次验证）
--   plans.update  修改套餐（L2，需二次验证）
--   plans.status  上架 / 下架（L2，需二次验证）
--
-- 三个写能力分开：改价和上架不是同一种权力——能改价的人不一定有权决定什么时候开卖。
-- 权限码与二次验证的 action_code 一一对应（与前面各轮同一约定）。
-- 只定义能力，不授予任何角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('plans.read','查看套餐（含草稿与已下架）',0),
 ('plans.create','新建套餐',2),
 ('plans.update','修改套餐（改价会作废未结报价）',2),
 ('plans.status','套餐上架或下架',2)
 ON CONFLICT(code) DO NOTHING;

-- 套餐写操作的幂等记录。与 admin_risk_mutations / admin_invite_mutations 同一形状但分表：
-- 一个 operation_id 落在哪类对象上，应当由表名直接回答。
CREATE TABLE admin_plan_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('plans.create','plans.update','plans.status')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_plan_mutations_admin_idx ON admin_plan_mutations (admin_user_id, created_at);

-- +goose Down
DROP TABLE admin_plan_mutations;
DELETE FROM admin_permissions WHERE code IN ('plans.read','plans.create','plans.update','plans.status');
