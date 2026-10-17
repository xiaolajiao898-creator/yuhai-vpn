package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuhai-vpn/server/internal/auth"
	"github.com/yuhai-vpn/server/internal/clock"
	"github.com/yuhai-vpn/server/internal/invite"
)

// Binding and historical records remain available while rewards are paused.
// The same runtime reward gate is configured for app-api and worker.
func appInviteHandler(pool *pgxpool.Pool, clk clock.Clock, mw *auth.Middleware, rewardsEnabled bool, baseURL string) *invite.Handler {
	return &invite.Handler{
		Svc:    &invite.Service{Pool: pool, Clock: clk, InviteBaseURL: baseURL, BindingEnabled: true, RewardsEnabled: rewardsEnabled},
		Binder: &invite.Binder{Pool: pool, Clock: clk},
		MW:     mw,
	}
}
