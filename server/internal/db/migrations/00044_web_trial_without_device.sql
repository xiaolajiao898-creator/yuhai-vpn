-- +goose Up
-- 网页登录不绑定设备。试用仍然每个账户一次，网页领取时不占用 devices 行。
-- App 领取继续写入 device_id 和 install_id。两者必须同时有或同时没有。

ALTER TABLE trial_grants ALTER COLUMN device_id DROP NOT NULL;
ALTER TABLE trial_grants ALTER COLUMN install_id DROP NOT NULL;

ALTER TABLE trial_grants DROP CONSTRAINT IF EXISTS trial_grants_client_pair;
ALTER TABLE trial_grants ADD CONSTRAINT trial_grants_client_pair
  CHECK ((device_id IS NULL) = (install_id IS NULL));

-- +goose Down
DELETE FROM trial_grants WHERE device_id IS NULL OR install_id IS NULL;
ALTER TABLE trial_grants DROP CONSTRAINT IF EXISTS trial_grants_client_pair;
ALTER TABLE trial_grants ALTER COLUMN device_id SET NOT NULL;
ALTER TABLE trial_grants ALTER COLUMN install_id SET NOT NULL;
