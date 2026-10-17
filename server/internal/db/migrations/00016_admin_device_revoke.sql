-- +goose Up
-- Operation receipts are retained. No raw request, reason, credential or token
-- is stored here; the token digest only permits the exact completed operation
-- to be read again while the original proof remains valid.
CREATE TABLE admin_device_mutations (
  admin_user_id BIGINT NOT NULL REFERENCES admin_users(id),
  operation_id UUID NOT NULL,
  request_hash CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  user_id BIGINT NOT NULL REFERENCES users(id),
  device_id BIGINT NOT NULL REFERENCES devices(id),
  session_id BIGINT NOT NULL REFERENCES admin_sessions(id),
  proof_hash CHAR(64) NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
  response JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY(admin_user_id, operation_id)
);

-- Define capability only; no production role or administrator is granted it.
INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('devices.revoke','撤销用户设备及访问凭据',2)
 ON CONFLICT(code) DO NOTHING;

-- +goose Down
SELECT 1;
