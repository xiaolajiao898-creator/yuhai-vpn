-- name: CreateUser :one
INSERT INTO users (account_no, secret_lookup, invite_code, register_channel, register_ip_hash, register_ip_prefix_hash)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserBySecretLookup :one
SELECT * FROM users WHERE secret_lookup = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByInviteCode :one
SELECT id, status FROM users WHERE invite_code = $1;

-- name: CreateDevice :one
INSERT INTO devices (user_id, device_uid, pubkey, platform, model, os_version, app_version, fingerprint_hash, display_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: CountActiveDevices :one
SELECT count(*) FROM devices WHERE user_id = $1 AND status = 'ACTIVE';

-- name: GetActiveDeviceByUID :one
SELECT * FROM devices WHERE device_uid = $1 AND status = 'ACTIVE';

-- name: InsertDeviceBindLog :exec
INSERT INTO device_bind_logs (user_id, device_id, action, operator_type, operator_id, ip_hash, detail, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (idempotency_key) DO NOTHING;
