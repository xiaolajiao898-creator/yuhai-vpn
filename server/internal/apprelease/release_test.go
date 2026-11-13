package apprelease

import "testing"

func TestAllowedURL(t *testing.T) {
	ok := []string{
		"https://yhvpn.io/download/builds/YuhaiVPN-Windows-1.1.0.exe",
		"https://www.yhvpn.io/download/latest/android",
		"https://api.yhvpn.io/v1/app/download/windows",
	}
	for _, raw := range ok {
		if !AllowedURL(raw) {
			t.Fatalf("expected allowed: %s", raw)
		}
	}
	bad := []string{
		"http://yhvpn.io/a.exe",
		"https://evil.yhvpn.io/a.exe",
		"https://yhvpn.io.evil.com/a.exe",
		"https://user:pass@yhvpn.io/a.exe",
		"https://yhvpn.io:8443/a.exe",
		"https://example.com/a.exe",
	}
	for _, raw := range bad {
		if AllowedURL(raw) {
			t.Fatalf("expected rejected: %s", raw)
		}
	}
}

func TestTokenOK(t *testing.T) {
	if tokenOK("Bearer secret-token", "") || tokenOK("", "secret-token") || tokenOK("Bearer other-token", "secret-token") {
		t.Fatal("empty or wrong token was accepted")
	}
	if !tokenOK("Bearer secret-token", "secret-token") {
		t.Fatal("matching token was rejected")
	}
}
