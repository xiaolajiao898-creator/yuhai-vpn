-- name: EnqueueOutbox :exec
INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload, next_retry_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (event_type, aggregate_type, aggregate_id) DO NOTHING;

-- name: ClaimOutboxBatch :many
-- 调用方校验 batch 与租约，并传入同一个业务时钟的 business_time / lease_until。
-- 返回的 attempts 是该次领取的版本；处理器必须事务幂等，租约到期可能重复投递。
UPDATE outbox_events
   SET attempts = attempts + 1,
       next_retry_at = sqlc.arg('lease_until')::timestamptz
 WHERE status = 'PENDING' AND id IN (
   SELECT id FROM outbox_events
    WHERE status = 'PENDING' AND next_retry_at <= sqlc.arg('business_time')::timestamptz
      AND event_type = ANY(sqlc.arg('event_types')::text[])
    ORDER BY next_retry_at, id
    LIMIT sqlc.arg('batch')
    FOR UPDATE SKIP LOCKED
 )
RETURNING *;

-- name: MarkOutboxDone :exec
-- 零行是陈旧租约或已终结状态，不能去掉版本条件重试。数据库错误必须向上传递。
UPDATE outbox_events SET status = 'DONE', last_error = NULL
 WHERE id = sqlc.arg('id') AND attempts = sqlc.arg('expected_attempt') AND status = 'PENDING';

-- name: MarkOutboxFailed :exec
UPDATE outbox_events
   SET last_error = sqlc.arg('last_error'),
       status = CASE WHEN attempts >= sqlc.arg('max_attempts') THEN 'DEAD' ELSE 'PENDING' END,
       next_retry_at = sqlc.arg('retry_at')::timestamptz
 WHERE id = sqlc.arg('id') AND attempts = sqlc.arg('expected_attempt') AND status = 'PENDING';
