-- +goose Up
-- 节点登记的写能力（新建分组 / 新建节点 / 修改节点 / 新增入站）。
--
-- 与 nodes.read 分开定义：能看节点清单和能改节点配置是两件事。改配置会直接影响
-- 用户看到什么、连到哪台机器，因此按 L1 对待，且需要独立授予。
--
-- 这里**只定义能力，不授予任何正式角色或管理员**——与 00015/00016/00017/00021 一致。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('nodes.write','新建与修改节点登记信息（不包含吊销与注册令牌）',1)
 ON CONFLICT(code) DO NOTHING;

-- 节点写操作的幂等记录。与 admin_device_mutations 同样的用途：同一 operation_id
-- 重复提交返回首次结果，绝不重复建出第二个节点/分组/入站。
--
-- request_hash 让「同一个 operation_id 配不同请求体」变成 40901 而不是静默返回旧结果——
-- 那通常意味着调用方复用了幂等键，悄悄返回旧结果会让人以为新的修改生效了。
CREATE TABLE admin_node_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('nodes.group_create','nodes.create','nodes.update','nodes.inbound_create')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  -- 用 JSON 而不是 JSONB：幂等重放必须返回与首次**逐字节相同**的响应体。
  -- JSONB 会重新排版（键序与空白都可能变），让调用方以为内容变了。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_node_mutations_admin_idx ON admin_node_mutations (admin_user_id, created_at);

-- +goose Down
DROP TABLE admin_node_mutations;
