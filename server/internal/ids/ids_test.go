package ids

import (
	"regexp"
	"testing"
	"time"
)

func TestGenerators(t *testing.T) {
	no, _ := NewAccountNo()
	if !regexp.MustCompile(`^[1-9][0-9]{9}$`).MatchString(no) {
		t.Fatalf("account no %q", no)
	}
	sec, _ := NewAccountSecret()
	if _, err := NormalizeSecret(sec); err != nil {
		t.Fatalf("secret %q 无法通过自身校验", sec)
	}
	if n, err := NormalizeSecret(" yh7kq2mx9rtahd4pwn3e8c "); err != nil || n != "YH-7KQ2-MX9R-TAHD-4PWN-3E8C" {
		t.Fatalf("NormalizeSecret=%q err=%v", n, err)
	}
	if _, err := NormalizeSecret("YH-7KQ2-MX9R"); err == nil {
		t.Fatal("短密钥应失败")
	}
	code, _ := NewInviteCode()
	if len(code) != 6 || regexp.MustCompile(`[01ILO]`).MatchString(code) {
		t.Fatalf("invite code %q", code)
	}
	on, _ := NewOrderNo(time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC))
	if !regexp.MustCompile(`^YH260911[2-9A-Z]{8}$`).MatchString(on) {
		t.Fatalf("order no %q", on)
	}
	if SecretLookup([]byte("p"), "a") == SecretLookup([]byte("q"), "a") {
		t.Fatal("pepper 未参与 HMAC")
	}
}
