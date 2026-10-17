package nodewire

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

type WGLease struct {
	Schema       int        `json:"schema"`
	RequestNonce string     `json:"request_nonce"`
	IssuedAt     string     `json:"issued_at"`
	ExpiresAt    string     `json:"expires_at"`
	Checksum     string     `json:"checksum"`
	Manifest     WGManifest `json:"manifest"`
}

var wgNonce = regexp.MustCompile(`^[a-f0-9]{32}$`)
var wgTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?Z$`)

func ValidWGNonce(s string) bool { return wgNonce.MatchString(s) }

// ParseWGLease never accepts a raw timeless manifest as a current authorization.
func ParseWGLease(raw []byte, nodeID string, now time.Time) (WGLease, error) {
	var l WGLease
	bad := errors.New("invalid or expired WireGuard lease")
	if len(raw) > 1048576 || StrictJSON(raw, &l) != nil || l.Schema != 1 || !ValidWGNonce(l.RequestNonce) || !wgTime.MatchString(l.IssuedAt) || !wgTime.MatchString(l.ExpiresAt) {
		return l, bad
	}
	issued, e := time.Parse(time.RFC3339Nano, l.IssuedAt)
	if e != nil {
		return l, bad
	}
	expiry, e := time.Parse(time.RFC3339Nano, l.ExpiresAt)
	if e != nil {
		return l, bad
	}
	if !expiry.After(issued) || expiry.Sub(issued) > 30*time.Second || now.Before(issued) || !now.Before(expiry) {
		return l, bad
	}
	mraw, _ := json.Marshal(l.Manifest)
	m, checksum, e := ParseWGManifest(mraw, nodeID)
	if e != nil || l.Checksum != checksum {
		return l, bad
	}
	l.Manifest = m
	return l, nil
}
