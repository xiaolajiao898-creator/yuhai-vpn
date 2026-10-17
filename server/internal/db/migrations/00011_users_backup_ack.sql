-- +goose Up
ALTER TABLE users ADD COLUMN backup_acknowledged_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users DROP COLUMN backup_acknowledged_at;
