package worldvpn

import (
	"strings"
	"testing"
)

func TestProbePortUsesReachableTCP(t *testing.T) {
	if ProbePort != 443 {
		t.Fatalf("probe port = %d, want 443", ProbePort)
	}
}

func TestParseProductionWireGuardConfig(t *testing.T) {
	text := "[Interface]\nPrivateKey = " + testKey(3) + "\nAddress = 10.8.0.2/24\nDNS = 1.1.1.1, 8.8.8.8\n\n[Peer]\nPublicKey = " + testKey(4) + "\nEndpoint = 203.0.113.10:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"
	m, err := parseWireGuardConfig(text)
	if err != nil {
		t.Fatal(err)
	}
	if m.Address != "10.8.0.2/32" || m.MTU != 1280 || m.Endpoint != "203.0.113.10:51820" || len(m.DNS) != 2 || m.DNS[0] != "1.1.1.1" || m.DNS[1] != "8.8.8.8" {
		t.Fatalf("%+v", m)
	}
	if m.PrivateKey != testKey(3) || m.ServerPublicKey != testKey(4) {
		t.Fatal("keys were rewritten")
	}
	for _, bad := range []string{
		strings.Replace(text, "Address = 10.8.0.2/24", "Address = 10.8.0.2/7", 1),
		strings.Replace(text, "Address = 10.8.0.2/24", "Address = 203.0.113.10/32", 1),
		strings.Replace(text, "Address = 10.8.0.2/24", "Address = 10.8.0.2", 1),
	} {
		if _, err := parseWireGuardConfig(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
