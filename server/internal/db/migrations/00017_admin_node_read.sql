-- +goose Up
-- Define a read capability only; existing roles receive no new access.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('nodes.read','查看节点状态与入站记录',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
SELECT 1;
