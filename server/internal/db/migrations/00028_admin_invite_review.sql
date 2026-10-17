-- +goose Up
-- 邀请关系与邀请奖励：只读列表 + 复核处置。
--
-- 背景：奖励一旦被风控判为可疑就停在 REVIEW，而 worker 只处理 PENDING
-- （changeRewardStatus 的 WHERE 写死 status='PENDING'）。在此之前 REVIEW 是一个
-- 没有出口的状态——既看不见也没人能放行或驳回。这次迁移开的就是那个出口。
--
--   invite.read           查看邀请关系与奖励（L0）
--   invite.reward_review  复核放行或驳回（L2，需二次验证）
--
-- 只定义能力，不授予任何角色或管理员——与前面各轮同一约定。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('invite.read','查看邀请关系与奖励',0),
 ('invite.reward_review','复核邀请奖励（放行或驳回）',2)
 ON CONFLICT(code) DO NOTHING;

-- 复核的幂等记录。与 admin_risk_mutations 形状相同但分表：一个 operation_id
-- 落在哪类对象上，应当由表名直接回答，而不是靠 action_code 反推。
CREATE TABLE admin_invite_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL,
  action_code   TEXT        NOT NULL CHECK (action_code = 'invite.reward_review'),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  session_id    BIGINT      NOT NULL,
  proof_hash    CHAR(64)    NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  -- 用 JSON 而非 JSONB：幂等重放必须返回与首次逐字节相同的响应体。
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (operation_id)
);

CREATE INDEX admin_invite_mutations_admin_idx ON admin_invite_mutations (admin_user_id, created_at);

-- 后台按状态翻页（运维真正要看的是 status='REVIEW' 那一批）。
-- 已有的两个索引一个是 beneficiary，一个是 PENDING 的到期偏索引，都不覆盖这个查询。
CREATE INDEX idx_invite_rewards_status ON invite_rewards (status, created_at DESC, id DESC);

-- +goose Down
DROP INDEX idx_invite_rewards_status;
DROP TABLE admin_invite_mutations;
DELETE FROM admin_permissions WHERE code IN ('invite.read','invite.reward_review');
