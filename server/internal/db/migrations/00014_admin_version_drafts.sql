-- +goose Up
-- Successful operations are retained for deterministic replay. They contain
-- no credentials, request bodies, download URLs, or release-note copies.
CREATE TABLE admin_version_mutations (
  admin_user_id BIGINT NOT NULL REFERENCES admin_users(id),
  operation_id UUID NOT NULL,
  request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  version_id BIGINT NOT NULL REFERENCES app_versions(id),
  result_hash CHAR(64) NOT NULL CHECK (result_hash ~ '^[a-f0-9]{64}$'),
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(admin_user_id, operation_id)
);

-- Define capabilities only. No existing role/admin receives either grant.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('app_versions.read','查看APP版本草稿和发布记录',0),
 ('app_versions.draft','新建和编辑APP版本草稿',2)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
SELECT 1;
