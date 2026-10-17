package nodeagent

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/nodewire"
)

type Sender interface {
	Send(context.Context, nodewire.Heartbeat) (nodewire.Receipt, error)
}
type Sampler interface {
	Sample(context.Context) (nodewire.Heartbeat, error)
}
type Runner struct {
	Store   *Store
	Sender  Sender
	Sampler Sampler
	Clock   clock.Clock
	mu      sync.Mutex
}
type Result struct{ State, Sequence string }

func (r *Runner) Step(ctx context.Context) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Store.lock == nil {
		return Result{}, errors.New("agent state lock is not held")
	}
	st := r.Store.state
	if st.Pending == nil {
		h, err := r.Sampler.Sample(ctx)
		if err != nil {
			return Result{State: "sampling_failed"}, nil
		}
		if err = ctx.Err(); err != nil {
			return Result{}, err
		}
		seq, err := integer(st.LastSequence)
		if err != nil || seq == math.MaxInt64 {
			return Result{}, errors.New("sequence exhausted or invalid")
		}
		h.Sequence = strconv.FormatInt(seq+1, 10)
		h.SentAt = r.Clock.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		if err = nodewire.ValidateHeartbeat(h); err != nil {
			return Result{}, errors.New("invalid sampler output")
		}
		st.LastSequence = h.Sequence
		st.Pending = &h
		if err = r.Store.write(st); err != nil {
			return Result{}, errors.New("cannot durably prepare heartbeat")
		}
	}
	out, err := r.Sender.Send(ctx, *st.Pending)
	if err != nil {
		var e *DeliveryError
		if errors.As(err, &e) {
			switch e.Kind {
			case "transient":
				return Result{"pending", st.LastSequence}, nil
			case "expired_request":
				st.Pending = nil
				if err = r.Store.write(st); err != nil {
					return Result{}, errors.New("cannot preserve expired sequence watermark")
				}
				return Result{"expired", st.LastSequence}, nil
			}
		}
		return Result{}, err
	}
	if !out.Accepted || out.Sequence != st.LastSequence {
		return Result{}, errors.New("receipt does not match pending heartbeat")
	}
	st.Pending = nil
	if err = r.Store.write(st); err != nil {
		return Result{}, errors.New("cannot durably acknowledge heartbeat")
	}
	return Result{"accepted", st.LastSequence}, nil
}
