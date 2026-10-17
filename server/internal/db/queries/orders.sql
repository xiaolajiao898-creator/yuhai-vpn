-- name: GetOrderByNo :one
SELECT * FROM orders WHERE order_no = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: CountPendingOrders :one
SELECT count(*) FROM orders WHERE user_id = $1 AND status IN ('PENDING', 'CONFIRMING');

-- name: ListOrdersByUser :many
SELECT * FROM orders
WHERE user_id = sqlc.arg('user_id')
  AND (sqlc.narg('before_id')::bigint IS NULL OR id < sqlc.narg('before_id'))
ORDER BY id DESC
LIMIT sqlc.arg('page_size');

-- name: TryLockAmount :one
-- 唯一尾数分配（方案 6.2）：返回行 = 分配成功；无行 = 槽位被占用
INSERT INTO amount_locks (receive_address_id, amount_micro, order_no, locked_until)
VALUES ($1, $2, $3, $4)
ON CONFLICT (receive_address_id, amount_micro) DO UPDATE
   SET order_no = EXCLUDED.order_no, locked_until = EXCLUDED.locked_until
 WHERE amount_locks.locked_until < now()
RETURNING order_no;

-- name: ExpirePendingOrders :many
-- worker 定时：只对到期的 PENDING 做条件更新
UPDATE orders SET status = 'EXPIRED', updated_at = now()
WHERE status = 'PENDING' AND expires_at < $1
RETURNING id, order_no;
