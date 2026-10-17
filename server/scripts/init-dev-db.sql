-- 仅开发环境：创建角色并授权。生产由 DBA 按 roles.sql 手工执行，口令来自密钥管理服务。
CREATE ROLE yuhai_migrator LOGIN PASSWORD 'yuhai_migrator';
CREATE ROLE yuhai_app      LOGIN PASSWORD 'yuhai_app';
CREATE ROLE yuhai_admin    LOGIN PASSWORD 'yuhai_admin';
CREATE ROLE yuhai_readonly LOGIN PASSWORD 'yuhai_readonly';
ALTER DATABASE yuhai OWNER TO yuhai_migrator;
GRANT ALL ON SCHEMA public TO yuhai_migrator;
-- 迁移后默认授权：新表自动对业务角色开放增查改（流水表的 UPDATE/DELETE 在 roles.sql 中回收）
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE ON TABLES TO yuhai_app, yuhai_admin;
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT SELECT ON TABLES TO yuhai_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO yuhai_app, yuhai_admin;
