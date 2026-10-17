package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yuhai-vpn/server/internal/outbox"
)

type fakeInvitationRewards struct {
	orderCalls, releaseCalls int
	ctx                      context.Context
	event                    outbox.Event
	now                      time.Time
	err                      error
}

func (f *fakeInvitationRewards) OnOrderCompleted(ctx context.Context, event outbox.Event) error {
	f.orderCalls++
	f.ctx, f.event = ctx, event
	return f.err
}

func (f *fakeInvitationRewards) ReleaseDue(ctx context.Context, now time.Time) error {
	f.releaseCalls++
	f.ctx, f.now = ctx, now
	return f.err
}

func TestInviteRewardsDisabledRegistersNeitherConsumerNorRelease(t *testing.T) {
	service := &fakeInvitationRewards{}
	jobs, handlers, err := wireInviteRewards(false, service)
	if err != nil || len(jobs) != 0 || len(handlers) != 0 || service.orderCalls != 0 || service.releaseCalls != 0 {
		t.Fatal("disabled rewards installed or invoked work", jobs, handlers, err)
	}
	for _, task := range schedule {
		if task.name == "invite_reward_release" {
			t.Fatal("release task must not be installed in the unconditional schedule")
		}
	}
	// No handler means no query, claim or acknowledgement. A nil database is
	// deliberate: any attempt to process queued events would fail this test.
	dispatcher := &outbox.Dispatcher{Handlers: handlers}
	if err := dispatcher.Run(context.Background(), time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal("disabled invitation dispatcher touched the queue", err)
	}
	if jobs, handlers, err := wireInviteRewards(false, nil); err != nil || len(jobs) != 0 || len(handlers) != 0 {
		t.Fatal("disabled gate should require no reward dependency", err)
	}
}

func TestInviteRewardsEnabledWiresOnlyExpectedCallbacks(t *testing.T) {
	sentinel := errors.New("fixture consumer failed")
	service := &fakeInvitationRewards{err: sentinel}
	jobs, handlers, err := wireInviteRewards(true, service)
	if err != nil || len(jobs) != 1 || len(handlers) != 1 {
		t.Fatal("enabled wiring incomplete", jobs, handlers, err)
	}
	if service.orderCalls != 0 || service.releaseCalls != 0 {
		t.Fatal("assembling the wiring executed reward business logic")
	}
	if jobs[0].name != "invite_reward_release" || jobs[0].interval != time.Minute || jobs[0].run == nil || handlers[outbox.OrderCompleted] == nil {
		t.Fatal("wrong reward callbacks installed", jobs, handlers)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	event := outbox.Event{ID: 7, Type: outbox.OrderCompleted, AggregateType: "order", AggregateID: 8, Attempts: 2}
	if err := handlers[outbox.OrderCompleted](ctx, event); !errors.Is(err, sentinel) {
		t.Fatal("consumer error swallowed", err)
	}
	if service.orderCalls != 1 || service.event.ID != event.ID || service.event.AggregateID != event.AggregateID || service.event.Attempts != event.Attempts || service.ctx != ctx {
		t.Fatal("consumer input was not forwarded", service)
	}
	now := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := jobs[0].run(ctx, now); !errors.Is(err, sentinel) {
		t.Fatal("release error swallowed", err)
	}
	if service.releaseCalls != 1 || service.now != now || service.ctx != ctx {
		t.Fatal("release context or business time not forwarded", service)
	}
	// Building a later disabled runtime must not reuse registrations from an
	// earlier enabled runtime through shared maps or global schedule mutation.
	if jobs, handlers, err := wireInviteRewards(false, service); err != nil || len(jobs) != 0 || len(handlers) != 0 {
		t.Fatal("enabled registrations leaked across runtime construction", err)
	}
}

func TestInviteRewardsEnabledRequiresService(t *testing.T) {
	if jobs, handlers, err := wireInviteRewards(true, nil); err == nil || len(jobs) != 0 || len(handlers) != 0 {
		t.Fatal("enabled rewards accepted a missing dependency", err)
	}
}
