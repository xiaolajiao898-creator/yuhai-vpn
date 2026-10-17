package main

import (
	"context"
	"errors"
	"time"

	"github.com/yuhai-vpn/server/internal/outbox"
)

type invitationRewards interface {
	OnOrderCompleted(context.Context, outbox.Event) error
	ReleaseDue(context.Context, time.Time) error
}

// wireInviteRewards only assembles callbacks. It neither invokes them nor
// changes reward rules. The disabled branch must omit both paths: replacing a
// consumer with a no-op would incorrectly acknowledge queued invitation facts.
func wireInviteRewards(enabled bool, service invitationRewards) ([]job, map[outbox.EventType]outbox.Handler, error) {
	if !enabled {
		return nil, nil, nil
	}
	if service == nil {
		return nil, nil, errors.New("enabled invitation rewards require a service")
	}
	return []job{{"invite_reward_release", time.Minute, service.ReleaseDue}},
		map[outbox.EventType]outbox.Handler{outbox.OrderCompleted: service.OnOrderCompleted}, nil
}
