-- name: ListEnabledRedeemCardTypes :many
SELECT code, name, duration_days, tier, device_limit, traffic_bytes, enabled
  FROM redeem_card_types
 WHERE enabled
 ORDER BY duration_days, code;

-- name: GetRedeemCodeByHash :one
SELECT id, code_hash, batch_id, card_type, duration_days, tier, device_limit, traffic_bytes, status,
       redeemed_by, redeemed_at
  FROM redeem_codes
 WHERE code_hash = $1;

-- name: MarkRedeemCodeUsed :execrows
UPDATE redeem_codes
   SET status = 'used', redeemed_by = $2, redeemed_at = $3
 WHERE id = $1 AND status = 'unused';
