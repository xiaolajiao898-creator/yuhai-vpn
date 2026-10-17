// Package admin implements the management API's two-step password/TOTP login,
// database-backed challenge and session lifecycle, CSRF and permission gates.
//
// Password challenges are random single-use tokens (two-minute TTL), stored only
// as hashes. MFA verification serializes on the administrator row; advancing the
// accepted TOTP counter, consuming the challenge and creating a session commit
// atomically. Failed credential checks are committed, so a rolled-back login
// cannot reset the five-failure, fifteen-minute account lockout.
//
// Cookies are opaque random bearer tokens. Only their hashes are persisted.
// Authentication enforces eight-hour absolute and thirty-minute idle limits and
// re-reads role permissions on every request. The caller must disable untrusted
// forwarded headers, use SecureCookies with HTTPS and restrict the admin service
// to its management network. No default administrator account is created.
//
// This package does not implement L2 step-up, L3 dual approval or audit hashing;
// callers must not treat this authentication layer as authority for such writes.
package admin
