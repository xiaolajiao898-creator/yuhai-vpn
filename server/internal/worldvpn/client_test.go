package worldvpn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testKey(fill byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestClientSandboxSuccessIsFormURLEncoded(t *testing.T) {
	var gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "listServers":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action": "getServers", "result": "success",
				"servers": []any{[]any{43, "Australia S1", "203.0.113.10"}},
			})
		case "addVPN":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action": "createVPN", "result": "success",
				"vpnUsername": "sampleUsername", "vpnPassword": "samplePassword",
			})
		case "getCredit":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "credit": 99})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := NewClient(Settings{BaseURL: srv.URL, Username: "reseller-user", Password: "reseller-secret", Sandbox: true})
	lines, err := c.ListServers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].ID != 43 || lines[0].Name != "Australia S1" || lines[0].IP != "203.0.113.10" {
		t.Fatalf("lines: %+v", lines)
	}
	if gotType != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type %q", gotType)
	}
	if strings.Contains(gotBody, "Content-Disposition") || strings.Contains(gotType, "multipart") {
		t.Fatalf("request was multipart: %s %s", gotType, gotBody)
	}
	for _, want := range []string{"resellerUsername=reseller-user", "resellerPassword=reseller-secret", "mode=test"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body missing %s: %s", want, gotBody)
		}
	}
	acc, err := c.AddVPN(context.Background(), "yh8302571644", "client-pass", 1)
	if err != nil || acc.Username != "sampleUsername" || acc.Password != "samplePassword" {
		t.Fatalf("addVPN: %+v %v", acc, err)
	}
	credit, err := c.GetCredit(context.Background())
	if err != nil || credit != 99 {
		t.Fatalf("credit %v %v", credit, err)
	}
}

func TestClientLiveOmitsSandboxMode(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "credit": 1})
	}))
	defer srv.Close()
	c := NewClient(Settings{BaseURL: srv.URL, Username: "u", Password: "p", Sandbox: false, LiveConfirm: true})
	if _, err := c.GetCredit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "mode=") {
		t.Fatalf("live request included mode: %s", body)
	}
}

func TestClientErrorAndMissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, "vpnUsername=missing-pass"):
			_ = json.NewEncoder(w).Encode(map[string]any{"action": "createVPN", "result": "success", "vpnUsername": "only-user"})
		case strings.Contains(body, "list-broken"):
			_ = json.NewEncoder(w).Encode(map[string]any{"action": "getServers", "result": "success"})
		case strings.Contains(body, "resellerPassword=reseller-secret"):
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "error", "message": "some options not posted, reseller-secret"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "error", "message": "some options not posted"})
		}
	}))
	defer srv.Close()
	c := NewClient(Settings{BaseURL: srv.URL, Username: "list-broken", Password: "reseller-secret", Sandbox: true})
	_, err := c.ListServers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "缺少 servers") {
		t.Fatalf("missing servers: %v", err)
	}
	_, err = c.AddVPN(context.Background(), "missing-pass", "x", 1)
	if err == nil || !strings.Contains(err.Error(), "缺少 vpnPassword") {
		t.Fatalf("missing password field: %v", err)
	}
	c.Username = "reseller-user"
	_, err = c.GetCredit(context.Background())
	if err == nil || !strings.Contains(err.Error(), "some options not posted") {
		t.Fatalf("error response: %v", err)
	}
	if strings.Contains(err.Error(), "reseller-secret") {
		t.Fatalf("password leaked in error: %v", err)
	}
}

func TestParseWireGuardConfig(t *testing.T) {
	good := "[Interface]\nPrivateKey = " + testKey(1) + "\nAddress = 10.0.0.2/32\nDNS = 1.1.1.1, 8.8.8.8\n\n[Peer]\nPublicKey = " + testKey(2) + "\nEndpoint = vpn.example.com:51820\n"
	m, err := parseWireGuardConfig(good)
	if err != nil {
		t.Fatal(err)
	}
	if m.Address != "10.0.0.2/32" || m.Endpoint != "vpn.example.com:51820" || m.MTU != 1280 || len(m.DNS) != 2 || m.PrivateKey == m.ServerPublicKey {
		t.Fatalf("%+v", m)
	}
	for _, bad := range []string{
		"",
		strings.Replace(good, "PrivateKey = "+testKey(1), "PrivateKey = ", 1),
		strings.Replace(good, "Address = 10.0.0.2/32", "Address = 8.8.8.8/32", 1),
		strings.Replace(good, "Endpoint = vpn.example.com:51820", "Endpoint = ", 1),
		strings.Replace(good, "DNS = 1.1.1.1, 8.8.8.8", "DNS = ", 1),
	} {
		if _, err := parseWireGuardConfig(bad); err == nil {
			t.Fatalf("accepted bad config %q", bad)
		}
	}
}

func TestClientWireGuardRoundTrip(t *testing.T) {
	cfg := "[Interface]\nPrivateKey = " + testKey(3) + "\nAddress = 10.8.0.2/32\nDNS = 1.1.1.1\nMTU = 1280\n\n[Peer]\nPublicKey = " + testKey(4) + "\nEndpoint = 203.0.113.10:51820\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "addWireGuardPeer":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success", "wireguardId": "123", "clientIp": "10.8.0.2/32",
				"dns": "1.1.1.1", "server": map[string]any{"name": "Example", "publicKey": testKey(4)},
			})
		case "getWireGuardConfig":
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "vpnUsername=broken") {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "config": cfg})
		case "deleteWireGuardPeer":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "Config deleted."})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "error", "message": "some options not posted"})
		}
	}))
	defer srv.Close()
	c := NewClient(Settings{BaseURL: srv.URL, Username: "u", Password: "p", Sandbox: true})
	peer, err := c.AddWireGuardPeer(context.Background(), "yh1", 43)
	if err != nil || peer.WireGuardID != "123" || peer.ClientIP != "10.8.0.2/32" {
		t.Fatalf("%+v %v", peer, err)
	}
	m, err := c.GetWireGuardConfig(context.Background(), "yh1", "123")
	if err != nil || m.PrivateKey != testKey(3) || m.Endpoint != "203.0.113.10:51820" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := c.GetWireGuardConfig(context.Background(), "broken", "1"); err == nil || !strings.Contains(err.Error(), "缺少 config") {
		t.Fatalf("missing config: %v", err)
	}
	// empty wireguard id still posts getWireGuardConfig; the handler above only matches exact path.
	// Force a missing config body.
	if err := c.DeleteWireGuardPeer(context.Background(), "yh1", "123", true); err != nil {
		t.Fatal(err)
	}
}

func TestMonthsAndSettings(t *testing.T) {
	if n, ok := MonthsForDays(30); !ok || n != 1 {
		t.Fatalf("30 days: %d %v", n, ok)
	}
	if n, ok := MonthsForDays(90); !ok || n != 3 {
		t.Fatalf("90 days: %d %v", n, ok)
	}
	if n, ok := MonthsForDays(365); !ok || n != 12 {
		t.Fatalf("365 days: %d %v", n, ok)
	}
	if _, ok := MonthsForDays(7); ok {
		t.Fatal("7 days must not map")
	}
	t.Setenv("WORLDVPN_SANDBOX", "")
	t.Setenv("WORLDVPN_RESELLER_USERNAME", "")
	t.Setenv("WORLDVPN_RESELLER_PASSWORD", "")
	t.Setenv("WORLDVPN_LIVE_CONFIRM", "")
	t.Setenv("WORLDVPN_API_BASE", "")
	t.Setenv("WORLDVPN_SYNC_AFTER", "")
	s, err := LoadSettings()
	if err != nil || !s.Sandbox || s.Enabled() {
		t.Fatalf("default: %+v %v", s, err)
	}
	t.Setenv("WORLDVPN_RESELLER_USERNAME", "user")
	t.Setenv("WORLDVPN_RESELLER_PASSWORD", "secret")
	s, err = LoadSettings()
	if err != nil || !s.Enabled() || !s.Sandbox {
		t.Fatalf("sandbox with creds: %+v %v", s, err)
	}
	t.Setenv("WORLDVPN_SANDBOX", "false")
	s, err = LoadSettings()
	if err != nil || s.Enabled() {
		t.Fatalf("live without confirm must stay disabled: %+v %v", s, err)
	}
	t.Setenv("WORLDVPN_LIVE_CONFIRM", "yes")
	s, err = LoadSettings()
	if err != nil || !s.Enabled() || s.Sandbox {
		t.Fatalf("explicit live: %+v %v", s, err)
	}
	t.Setenv("WORLDVPN_SANDBOX", "yes")
	if _, err := LoadSettings(); err == nil {
		t.Fatal("invalid sandbox value")
	}
}
