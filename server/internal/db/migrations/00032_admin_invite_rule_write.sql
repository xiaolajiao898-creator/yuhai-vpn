-- +goose Up
-- 邀请奖励规则修改（PATCH /admin/v1/invite-rules/{id}）。
--
-- invite_reward_rules 此前同样**没有任何接口能写**：改一条奖励规则要连库执行 UPDATE。
-- 而这条链路的代价比配置更直接——发放侧对形状不合格的规则并不是跳过，而是让**每一笔**
-- 奖励都带着 INVALID_RULE / DAILY_CAP_REQUIRED 落进 REVIEW 等人工复核，
-- 也就是把第 18 节刚解开的那个队列重新灌满。
--
-- 能力单列，L2 + 二次验证。只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('invite.rule_write','修改邀请奖励规则',2)
 ON CONFLICT(code) DO NOTHING;

-- 复用 00028 建的 admin_invite_mutations：同属邀请域，与 admin_risk_mutations 同时容纳
-- blacklist 与 users.status 两类目标是同一个先例。只需放开 action_code 的取值。
ALTER TABLE admin_invite_mutations DROP CONSTRAINT admin_invite_mutations_action_code_check;
ALTER TABLE admin_invite_mutations ADD CONSTRAINT admin_invite_mutations_action_code_check
  CHECK (action_code IN ('invite.reward_review','invite.rule_write'));

-- +goose Down
ALTER TABLE admin_invite_mutations DROP CONSTRAINT admin_invite_mutations_action_code_check;
ALTER TABLE admin_invite_mutations ADD CONSTRAINT admin_invite_mutations_action_code_check
  CHECK (action_code = 'invite.reward_review');
DELETE FROM admin_permissions WHERE code='invite.rule_write';
