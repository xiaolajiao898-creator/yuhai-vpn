package nodecontrol

import (
	"strings"
	"testing"
	"time"
)

func TestRevokedBindingCannotReviveThroughPreviousCertificate(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	current, previous := strings.Repeat("a", 64), strings.Repeat("b", 64)
	until := now.Add(24 * time.Hour)
	c := nodeCert{Current: &current, Prev: &previous, Until: &until}
	if !c.accepts(current, now) || !c.accepts(previous, now) {
		t.Fatal("valid overlap rejected")
	}
	if c.accepts(previous, until) {
		t.Fatal("expired previous certificate accepted")
	}
	c.Current = nil
	if c.accepts(previous, now) {
		t.Fatal("revoked binding revived from old fingerprint")
	}
	blank := ""
	c.Current = &blank
	if c.accepts(previous, now) || c.accepts("", now) {
		t.Fatal("empty binding accepted")
	}
}
