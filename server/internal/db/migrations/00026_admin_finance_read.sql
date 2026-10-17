-- +goose Up
-- 资金只读能力：订单、链上转账、到账异常、收款地址、扫描游标。
--
-- 与 users.read 分开定义：能看用户资料和能看全量资金流水是两件事。这里是**只读**能力，
-- 风险等级 0，但它能看到所有用户的订单金额与付款地址，因此仍需单独授予，不随便给。
--
-- 只定义能力，不授予任何正式角色或管理员——与 00015/00016/00017/00021/00023/00024 一致。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('finance.read','查看订单、链上转账、到账异常、收款地址与扫描游标',0)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
DELETE FROM admin_permissions WHERE code='finance.read';
