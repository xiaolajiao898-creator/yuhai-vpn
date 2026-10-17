-- +goose Up
-- 账号改为「用户自己的 TRON 地址 + 自设密码」。
-- 地址只存 HMAC 查找键和加密副本：数据库泄露时不能直接把地址和本服务对应起来。
-- 原有账户号/账户密钥字段保留（内部标识与旧客户端兼容），新注册不再向用户展示账户密钥。
ALTER TABLE users
  ADD COLUMN login_lookup        CHAR(64) UNIQUE,
  ADD COLUMN login_address_enc   BYTEA,
  ADD COLUMN password_hash       TEXT,
  ADD COLUMN password_changed_at TIMESTAMPTZ;
ALTER TABLE users ADD CONSTRAINT users_login_complete
  CHECK ((login_lookup IS NULL) = (login_address_enc IS NULL) AND (login_lookup IS NULL) = (password_hash IS NULL));

-- 电脑版客户端
ALTER TABLE devices DROP CONSTRAINT devices_platform_check;
ALTER TABLE devices ADD CONSTRAINT devices_platform_check
  CHECK (platform IN ('ANDROID', 'IOS', 'WINDOWS', 'MACOS'));

-- 忘记密码：从注册地址向官方地址转一笔「精确金额」的 TRX 证明本人。
-- 每个挑战的金额不同（1 TRX + 随机零头），防止别人抢先用你的转账冒领。
CREATE TABLE password_reset_challenges (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      BIGINT      NOT NULL REFERENCES users(id),
  pay_to       TEXT        NOT NULL,
  amount_sun   BIGINT      NOT NULL CHECK (amount_sun > 0),
  status       TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'COMPLETED', 'EXPIRED')),
  tx_id        TEXT        UNIQUE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX password_reset_pending_amount
  ON password_reset_challenges (user_id, amount_sun) WHERE status = 'PENDING';
CREATE INDEX password_reset_user_created ON password_reset_challenges (user_id, created_at DESC);
REVOKE ALL ON password_reset_challenges FROM PUBLIC, yuhai_readonly;
GRANT SELECT, INSERT, UPDATE ON password_reset_challenges TO yuhai_app;
REVOKE INSERT, UPDATE, DELETE ON password_reset_challenges FROM yuhai_admin;

-- +goose Down
DROP TABLE password_reset_challenges;
ALTER TABLE devices DROP CONSTRAINT devices_platform_check;
ALTER TABLE devices ADD CONSTRAINT devices_platform_check CHECK (platform IN ('ANDROID', 'IOS'));
ALTER TABLE users DROP CONSTRAINT users_login_complete;
ALTER TABLE users DROP COLUMN password_changed_at, DROP COLUMN password_hash,
  DROP COLUMN login_address_enc, DROP COLUMN login_lookup;
