-- 数据库角色与权限（DBA 在迁移后执行一次；业务服务绝不使用 yuhai_migrator 连接）
-- 迁移角色：表的所有者，只在执行迁移时使用，业务服务不使用
CREATE ROLE yuhai_migrator LOGIN;

-- 业务服务（app-api、worker）
CREATE ROLE yuhai_app LOGIN;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO yuhai_app;
REVOKE UPDATE, DELETE, TRUNCATE
  ON balance_ledger, subscription_ledger, device_bind_logs, admin_audit_logs
  FROM yuhai_app;
REVOKE DELETE, TRUNCATE ON chain_transfers, orders FROM yuhai_app;

-- 后台服务（admin-api）：同样不能修改流水，只能通过审批执行业务函数写入新流水
CREATE ROLE yuhai_admin LOGIN;
-- 授权方式同上，另按需收紧

-- 报表与审计只读
CREATE ROLE yuhai_readonly LOGIN;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO yuhai_readonly;

-- 必须最后执行；否则前面的 ALL TABLES 会重新开放管理员凭据和追加表。
-- 用 psql -v ON_ERROR_STOP=1 -f server/scripts/roles.sql 执行本文件。
\ir ../internal/db/migrations/00039_security_privileges.sql
