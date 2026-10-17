-- +goose Up
-- Read capabilities only. Do not grant them to any role or administrator.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('users.read','查看用户账户与套餐记录',0),
 ('users.ledgers.read','查看用户余额与权益流水',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
SELECT 1;
