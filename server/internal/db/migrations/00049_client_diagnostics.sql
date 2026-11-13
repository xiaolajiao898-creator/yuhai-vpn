-- +goose Up
-- 客户端连接失败时上报的诊断。只存阶段、错误原文、服务状态和去掉私钥后的日志摘要。
-- 不存 WireGuard 私钥、预共享密钥或用户口令。后台用 users.diagnostics 查看。
CREATE TABLE client_connect_diagnostics (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        BIGINT      NOT NULL,
  device_id      BIGINT      NOT NULL,
  app_version    TEXT        NOT NULL DEFAULT '',
  platform       TEXT        NOT NULL DEFAULT '',
  stage          TEXT        NOT NULL,
  error_code     TEXT        NOT NULL DEFAULT '',
  error_text     TEXT        NOT NULL DEFAULT '',
  service_state  TEXT        NOT NULL DEFAULT '',
  log_summary    TEXT        NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX client_connect_diagnostics_created
  ON client_connect_diagnostics (created_at DESC, id DESC);
CREATE INDEX client_connect_diagnostics_user
  ON client_connect_diagnostics (user_id, created_at DESC);

GRANT SELECT, INSERT ON client_connect_diagnostics TO yuhai_app;
GRANT USAGE, SELECT ON SEQUENCE client_connect_diagnostics_id_seq TO yuhai_app;
REVOKE UPDATE, DELETE, TRUNCATE ON client_connect_diagnostics FROM yuhai_app;
GRANT SELECT ON client_connect_diagnostics TO yuhai_admin;

INSERT INTO admin_permissions(code, name, risk_level) VALUES
 ('users.diagnostics', '查看客户端连接诊断', 0)
 ON CONFLICT(code) DO NOTHING;

INSERT INTO admin_role_permissions(role_id, permission_code)
 SELECT role_id, 'users.diagnostics'
   FROM admin_role_permissions
  WHERE permission_code = 'users.read'
 ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM admin_role_permissions WHERE permission_code = 'users.diagnostics';
DELETE FROM admin_permissions WHERE code = 'users.diagnostics';
DROP TABLE IF EXISTS client_connect_diagnostics;
