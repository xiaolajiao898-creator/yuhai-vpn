// Package outbox 定义领域事件：写库事务内落 outbox_events，worker 投递到消费者（方案 6.5、7.4）。
// 唯一约束只保证同一事实入队一次；租约到期会重复投递，处理器必须在自己的事务内幂等。
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/safelog"
)

type EventType string

const (
	OrderCompleted      EventType = "ORDER_COMPLETED"   // 触发：邀请首付奖励
	InviteRewardDue     EventType = "INVITE_REWARD_DUE" // 触发：冻结期满释放
	SubscriptionExpired EventType = "SUBSCRIPTION_EXPIRED"
)

type Event struct {
	ID            int64
	Type          EventType
	AggregateType string
	AggregateID   int64
	Payload       []byte
	Attempts      int
}

type Handler func(ctx context.Context, ev Event) error

// Enqueue 在调用方事务内写入事件；重复写入静默忽略。
func Enqueue(ctx context.Context, tx pgx.Tx, t EventType, aggregateType string, aggregateID int64, payload []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO outbox_events (event_type, aggregate_type, aggregate_id, payload)
		VALUES ($1, $2, $3, $4) ON CONFLICT (event_type, aggregate_type, aggregate_id) DO NOTHING`,
		string(t), aggregateType, aggregateID, payload)
	return err
}

// Dispatcher 租约式领取并投递。只领取已注册处理器的事件类型，未注册的类型留在队列里等待后续版本处理。
type Dispatcher struct {
	Pool        *pgxpool.Pool
	Handlers    map[EventType]Handler
	Batch       int
	Lease       time.Duration
	MaxAttempts int
}

// Run uses the supplied business time for this batch's claim and retry times.
// Attempts fence acknowledgements only: an expired handler may still execute,
// so handlers must deduplicate their effects transactionally, including retries
// after an uncertain acknowledgement. Do not mutate Handlers during Run.
func (d *Dispatcher) Run(ctx context.Context, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batch, lease, maxAttempts, err := d.options()
	if err != nil {
		return err
	}
	if len(d.Handlers) == 0 {
		return nil
	}
	if d.Pool == nil || now.IsZero() {
		return errors.New("outbox: database and business time are required")
	}
	types := make([]string, 0, len(d.Handlers))
	for t, handler := range d.Handlers {
		if t == "" || handler == nil {
			return errors.New("outbox: registered event type and handler must be nonempty")
		}
		types = append(types, string(t))
	}
	sort.Strings(types)
	rows, err := d.Pool.Query(ctx, `
		UPDATE outbox_events SET attempts = attempts + 1, next_retry_at = $2
		 WHERE status = 'PENDING' AND id IN (
		   SELECT id FROM outbox_events
		    WHERE status = 'PENDING' AND next_retry_at <= $4 AND event_type = ANY($3)
		    ORDER BY next_retry_at, id LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING id, event_type, aggregate_type, aggregate_id, payload, attempts`, batch, now.Add(lease), types, now)
	if err != nil {
		return fmt.Errorf("outbox claim: %w", err)
	}
	events, err := readEvents(rows)
	if err != nil {
		return fmt.Errorf("outbox claim rows: %w", err)
	}
	for _, ev := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		handlerErr := d.Handlers[ev.Type](ctx, ev)
		// Leave this and the unstarted events leased on cancellation. Retrying
		// them later is safer than acknowledging work of uncertain completion.
		if err := ctx.Err(); err != nil {
			return err
		}
		if handlerErr != nil {
			slog.ErrorContext(ctx, "outbox handler failed", "event", ev.Type, "id", ev.ID, "attempt", ev.Attempts, "err", safelog.Error(handlerErr))
			_, err = d.Pool.Exec(ctx, `
				UPDATE outbox_events SET last_error = $2,
				       status = CASE WHEN attempts >= $3 THEN 'DEAD' ELSE 'PENDING' END,
				       next_retry_at = $5
				 WHERE id = $1 AND attempts = $4 AND status = 'PENDING'`, ev.ID, "handler_failed", maxAttempts, ev.Attempts, now.Add(time.Duration(min(ev.Attempts, 10))*time.Minute))
			if err != nil {
				return fmt.Errorf("outbox persist failure for event %d attempt %d: %w", ev.ID, ev.Attempts, err)
			}
			continue
		}
		_, err = d.Pool.Exec(ctx, `UPDATE outbox_events SET status = 'DONE', last_error = NULL WHERE id = $1 AND attempts = $2 AND status = 'PENDING'`, ev.ID, ev.Attempts)
		if err != nil {
			return fmt.Errorf("outbox persist success for event %d attempt %d: %w", ev.ID, ev.Attempts, err)
		}
		// A zero-row update means a newer attempt or terminal state won. It is
		// expected after lease expiry; never retry the acknowledgement unfenced.
	}
	return nil
}

func readEvents(rows pgx.Rows) ([]Event, error) {
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var ev Event
		var t string
		if err := rows.Scan(&ev.ID, &t, &ev.AggregateType, &ev.AggregateID, &ev.Payload, &ev.Attempts); err != nil {
			return nil, err
		}
		ev.Type = EventType(t)
		events = append(events, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (d *Dispatcher) options() (batch int, lease time.Duration, maxAttempts int, err error) {
	batch, lease, maxAttempts = d.Batch, d.Lease, d.MaxAttempts
	if batch == 0 {
		batch = 50
	}
	if lease == 0 {
		lease = 5 * time.Minute
	}
	if maxAttempts == 0 {
		maxAttempts = 20
	}
	if batch < 1 || batch > 1000 || lease < time.Second || lease > time.Hour || maxAttempts < 1 || maxAttempts > 1000 {
		err = errors.New("outbox: batch must be 1..1000, lease 1s..1h, and max attempts 1..1000 (zero uses defaults)")
	}
	return
}
