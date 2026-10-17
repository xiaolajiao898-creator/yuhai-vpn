// Package invite provides invitation reads, guarded one-time binding and
// transactional reward generation/release. Reads never trigger rewards.
// App API binding is wired. Worker reward generation and release are gated by
// INVITE_REWARDS_ENABLED, default false. No rules default enabled.
// Rewards use unique source identity and invite_reward:{id} ledger keys, with
// grant and state transition in one transaction. See HANDOFF-ROUND7.md.
package invite
