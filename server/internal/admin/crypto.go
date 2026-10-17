package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const passwordPrefix = "$argon2id$v=19$m=65536,t=3,p=4$"

// HashPassword uses the project's fixed Argon2id policy. Account provisioning
// must additionally reject breached passwords using the operator's policy.
func HashPassword(password string) (string, error) {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must contain at least 12 characters and at most 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return passwordPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(hash), nil
}

// VerifyPassword rejects unknown/oversized parameters before allocating memory.
// It deliberately accepts only hashes created with the configured policy.
func VerifyPassword(encoded, password string) bool {
	if len(password) > 1024 || !strings.HasPrefix(encoded, passwordPrefix) || len(encoded) > 200 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(encoded, passwordPrefix), "$")
	if len(parts) != 2 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(parts[0])
	expected, e2 := base64.RawStdEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func validToken(s string) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func csrfToken(session string) string {
	m := hmac.New(sha256.New, []byte(session))
	_, _ = m.Write([]byte("yuhai-admin-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func validCSRF(session, supplied string) bool {
	return validToken(supplied) && subtle.ConstantTimeCompare([]byte(csrfToken(session)), []byte(supplied)) == 1
}

// TOTP secrets are encrypted as unpadded RFC 4648 base32 text. The returned step
// is persisted in the same transaction that creates the session (anti-replay).
func verifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return 0, false
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(secret, "=")))
	if err != nil || len(raw) < 20 || len(raw) > 64 || now.Unix() < 30 {
		return 0, false
	}
	step := now.Unix() / 30
	// Prefer the newest matching step if a rare six-digit collision occurs.
	for s := step + 1; s >= step-1; s-- {
		if s <= lastStep {
			continue
		}
		var counter [8]byte
		binary.BigEndian.PutUint64(counter[:], uint64(s))
		m := hmac.New(sha1.New, raw)
		_, _ = m.Write(counter[:])
		sum := m.Sum(nil)
		offset := sum[len(sum)-1] & 15
		value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
		expected := fmt.Sprintf("%06d", value%1000000)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}
func ipPrefix(ip string) (string, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", errors.New("invalid client IP")
	}
	addr = addr.Unmap()
	bits := 24
	if addr.Is6() {
		bits = 64
	}
	return netip.PrefixFrom(addr, bits).Masked().String(), nil
}
func sameNetwork(a, b string) bool {
	x, e1 := ipPrefix(a)
	y, e2 := ipPrefix(b)
	return e1 == nil && e2 == nil && x == y
}
func adminID(id int64) string { return strconv.FormatInt(id, 10) }
