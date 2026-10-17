package auth

import (
	"bytes"
	"testing"
	"time"
)

func TestJWTRoundTrip(t *testing.T) {
	s := NewSigner("k1", bytes.Repeat([]byte{7}, 32))
	now := time.Unix(1_789_110_000, 0)
	tok, err := s.Sign(AccessClaims{Subject: 42, DeviceID: 9, SecretVersion: 1, KeyThumbprint: "x", IssuedAt: now.Unix(), ExpiresAt: now.Add(AccessTokenTTL).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Verify(tok, now, false)
	if err != nil || c.Subject != 42 || c.DeviceID != 9 || c.KeyID != "k1" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	if _, err := s.Verify(tok, now.Add(16*time.Minute), false); err != ErrTokenExpired {
		t.Fatalf("want expired, got %v", err)
	}
	if _, err := s.Verify(tok, now.Add(16*time.Minute), true); err != nil {
		t.Fatalf("allowExpired: %v", err)
	}
	other := NewSigner("k1", bytes.Repeat([]byte{8}, 32))
	if _, err := other.Verify(tok, now, false); err != ErrTokenSignature {
		t.Fatalf("want signature error, got %v", err)
	}
	if _, err := s.Verify(tok[:len(tok)-2], now, false); err == nil {
		t.Fatal("tampered token accepted")
	}
}
