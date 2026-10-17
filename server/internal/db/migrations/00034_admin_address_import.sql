-- +goose Up
-- 收款地址导入（L3 双人审批）。
--
-- 在这之前，往 receive_addresses 里加地址的唯一办法是把 cmd/fundsign 打出来的 INSERT
-- 贴进 psql——谁有库的写权限，谁就能往决定「用户往哪儿付钱」的这张表里塞任何地址，
-- 签名字段爱填什么填什么。服务端此前**从未验过那个签名**。
--
-- 风险等级 3：与 app_versions.publish 同级，需要另一名管理员审批才会执行。
-- 只定义能力，不授予任何正式角色或管理员。
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('finance.address_import','导入资金系统签名的收款地址包',3)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
DELETE FROM admin_permissions WHERE code='finance.address_import';
