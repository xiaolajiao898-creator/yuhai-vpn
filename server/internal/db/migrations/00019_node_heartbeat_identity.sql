-- +goose Up
-- Existing receipts stay NULL: only a new accepted heartbeat establishes binding.
ALTER TABLE node_heartbeats ADD COLUMN cert_fingerprint CHAR(64)
  CHECK (cert_fingerprint IS NULL OR cert_fingerprint ~ '^[a-f0-9]{64}$');

-- +goose Down
ALTER TABLE node_heartbeats DROP COLUMN cert_fingerprint;
