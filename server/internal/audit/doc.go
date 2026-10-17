// Package audit implements the transactional, append-only administration audit
// chain. SHA-256 receives the ASCII previous hash followed by RFC 8785 JCS of
// every stored field except row_hash (including prev_hash). IDs are decimal JSON
// strings; timestamps are UTC with exactly six fractional digits. Snapshots use
// a deliberately restricted JCS subset: allowlisted flat objects containing
// strings, booleans and null, never JSON numbers. This preserves BIGINT precision
// without claiming an approximate floating-point encoder implements full JCS.
//
// Append must be the caller's final business write in a READ COMMITTED
// transaction. Its error must abort that transaction. Verification is scoped to
// the requested rows and their immediate predecessor in one fixed snapshot.
// Hashes do not provide an external trust anchor: privileged wholesale rewriting
// or truncating the unanchored tail cannot be proven absent. External WORM
// anchoring is not implemented here.
package audit
