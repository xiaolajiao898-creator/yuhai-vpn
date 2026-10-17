-- +goose Up
-- 流量统计只读能力。
--
-- 与 users.read / finance.read 分开定义：能看用户资料、能看资金流水、能看全站流量分布，
-- 是三件事。流量聚合会暴露「哪个用户在用、用了多少、走的哪个节点」，
-- 因此单独授予，不随 users.read 附赠。
--
-- 只定义能力，不授予任何正式角色或管理员——与 00015/00016/00017/00021/00023/00024/00026 一致。
--
-- 不需要新增表权限：traffic_daily 的 SELECT 已在 00022 授予 yuhai_app（那次是为上报入库），
-- 本轮只读复用它，不再扩权。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('traffic.read','查看流量统计（按日/用户/设备/节点聚合）',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
DELETE FROM admin_permissions WHERE code='traffic.read';
