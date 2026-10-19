-- +goose Up
-- 新用户 24 小时免费试用。
--
-- 每个账户只能领一次，每台设备也只能领一次。设备按三处身份去重：
--   1. devices.id（当前登录的设备行）
--   2. devices.fingerprint_hash（注册时如果带了指纹）
--   3. 客户端持久化、注册第二账户时也不轮换的 install_id
-- 幂等键 trial:user:{user_id} 写在 subscription_ledger 上，重复请求不会把到期时间再往后推。
--
-- 开关：system_configs.trial.enabled（json 布尔，默认 true）。
-- 进程环境变量 YUHAI_TRIAL_ENABLED=0 也可以关掉，不需要改库。

ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_change_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_change_type_check
  CHECK (change_type IN (
    'PURCHASE', 'RENEW', 'TIER_CHANGE', 'TRAFFIC_PACK', 'DEVICE_ADDON',
    'INVITE_REWARD', 'ADMIN_ADJUST', 'TRAFFIC_CONSUME_OVERFLOW',
    'PERIOD_RESET', 'EXPIRE', 'REVOKE', 'REDEEM', 'TRIAL'));

ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_source_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_source_type_check
  CHECK (source_type IN ('ORDER', 'INVITE_REWARD', 'ADMIN_APPROVAL', 'SYSTEM', 'REDEEM_CODE', 'TRIAL'));

CREATE TABLE trial_grants (
  user_id           BIGINT       PRIMARY KEY REFERENCES users(id),
  device_id         BIGINT       NOT NULL UNIQUE REFERENCES devices(id),
  install_id        UUID         NOT NULL UNIQUE,
  fingerprint_hash  CHAR(64),
  granted_at        TIMESTAMPTZ  NOT NULL,
  expire_at         TIMESTAMPTZ  NOT NULL,
  CHECK (fingerprint_hash IS NULL OR fingerprint_hash ~ '^[a-f0-9]{64}$'),
  CHECK (expire_at > granted_at)
);

CREATE UNIQUE INDEX trial_grants_fingerprint_uidx
  ON trial_grants (fingerprint_hash)
  WHERE fingerprint_hash IS NOT NULL;

INSERT INTO system_configs(key, value)
VALUES ('trial.enabled', 'true'::jsonb)
ON CONFLICT (key) DO NOTHING;

REVOKE ALL ON trial_grants FROM yuhai_app, PUBLIC;
GRANT SELECT, INSERT ON trial_grants TO yuhai_app;
REVOKE UPDATE, DELETE, TRUNCATE ON trial_grants FROM yuhai_app, yuhai_admin, PUBLIC;
GRANT SELECT ON trial_grants TO yuhai_readonly;

-- +goose Down
DROP TABLE IF EXISTS trial_grants;
DELETE FROM system_configs WHERE key = 'trial.enabled';
ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_change_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_change_type_check
  CHECK (change_type IN (
    'PURCHASE', 'RENEW', 'TIER_CHANGE', 'TRAFFIC_PACK', 'DEVICE_ADDON',
    'INVITE_REWARD', 'ADMIN_ADJUST', 'TRAFFIC_CONSUME_OVERFLOW',
    'PERIOD_RESET', 'EXPIRE', 'REVOKE', 'REDEEM'));
ALTER TABLE subscription_ledger DROP CONSTRAINT IF EXISTS subscription_ledger_source_type_check;
ALTER TABLE subscription_ledger ADD CONSTRAINT subscription_ledger_source_type_check
  CHECK (source_type IN ('ORDER', 'INVITE_REWARD', 'ADMIN_APPROVAL', 'SYSTEM', 'REDEEM_CODE'));
