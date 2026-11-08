-- +goose Up
-- 批发扣点已经成功、本地还没记下来时，重试不能再调一次 AddVPN/RenewVPN。
-- 把这次成功的批发账号记在开通记录上。不改兑换码、试用或收款表。

ALTER TABLE worldvpn_grants
  ADD COLUMN upstream_username TEXT,
  ADD COLUMN upstream_password_enc BYTEA;

ALTER TABLE worldvpn_grants
  ADD CONSTRAINT worldvpn_grants_upstream_pair
  CHECK ((upstream_username IS NULL) = (upstream_password_enc IS NULL));

ALTER TABLE worldvpn_grants
  ADD CONSTRAINT worldvpn_grants_upstream_name
  CHECK (upstream_username IS NULL OR char_length(upstream_username) BETWEEN 1 AND 64);

-- +goose Down
ALTER TABLE worldvpn_grants DROP CONSTRAINT IF EXISTS worldvpn_grants_upstream_name;
ALTER TABLE worldvpn_grants DROP CONSTRAINT IF EXISTS worldvpn_grants_upstream_pair;
ALTER TABLE worldvpn_grants DROP COLUMN IF EXISTS upstream_password_enc;
ALTER TABLE worldvpn_grants DROP COLUMN IF EXISTS upstream_username;
