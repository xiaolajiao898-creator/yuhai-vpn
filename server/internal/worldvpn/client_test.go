package worldvpn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	if _, err := c.GetWireGuardConfig(context.Background(), "broken", "1"); err == nil || !strings.Contains(err.Error(), "缺少 wireguard_config") {
		t.Fatalf("missing config: %v", err)
	}
	// empty wireguard id still posts getWireGuardConfig; the handler above only matches exact path.
	// Force a missing config body.
	if err := c.DeleteWireGuardPeer(context.Background(), "yh1", "123", true); err != nil {
		t.Fatal(err)
	}
}

func TestResponseFixtures(t *testing.T) {
	type accountCall struct {
		file    string
		user    string
		pass    string
		request []string
		call    func(*Client) (vpnAccount, error)
	}
	accounts := []accountCall{
		{
			file: "testdata/get_trial_documented.json", user: "sampleUsername", pass: "samplePassword",
			request: []string{"email=trial-user%40users.invalid"},
			call: func(c *Client) (vpnAccount, error) {
				return c.GetTrial(context.Background(), "trial-user@users.invalid")
			},
		},
		{
			file: "testdata/get_trial_username.json", user: "sampleUsername", pass: "samplePassword",
			call: func(c *Client) (vpnAccount, error) {
				return c.GetTrial(context.Background(), "trial-user@users.invalid")
			},
		},
		{
			file: "testdata/get_trial_nested_username.json", user: "sampleUsername", pass: "samplePassword",
			call: func(c *Client) (vpnAccount, error) {
				return c.GetTrial(context.Background(), "trial-user@users.invalid")
			},
		},
		{
			file: "testdata/add_vpn_documented.json", user: "sampleUsername", pass: "samplePassword",
			request: []string{"vpnUsername=yh8302571644", "vpnPassword=client-pass", "period=1"},
			call: func(c *Client) (vpnAccount, error) {
				return c.AddVPN(context.Background(), "yh8302571644", "client-pass", 1)
			},
		},
		{
			file: "testdata/add_vpn_username.json", user: "sampleUsername", pass: "samplePassword",
			call: func(c *Client) (vpnAccount, error) {
				return c.AddVPN(context.Background(), "yh8302571644", "client-pass", 1)
			},
		},
		{
			file: "testdata/add_vpn_both_pairs.json", user: "sampleUsername", pass: "samplePassword",
			call: func(c *Client) (vpnAccount, error) {
				return c.AddVPN(context.Background(), "yh8302571644", "client-pass", 1)
			},
		},
	}
	for _, tc := range accounts {
		t.Run(tc.file, func(t *testing.T) {
			c, got := fixtureClient(t, tc.file)
			acc, err := tc.call(c)
			if err != nil || acc.Username != tc.user || acc.Password != tc.pass {
				t.Fatalf("account %+v %v", acc, err)
			}
			for _, want := range tc.request {
				if !strings.Contains(*got, want) {
					t.Fatalf("request missing %s: %s", want, *got)
				}
			}
		})
	}

	lists := []struct {
		file string
		want [][3]string
	}{
		{"testdata/get_accounts_documented.json", [][3]string{{"1", "demotestingvpn", "2026-07-19"}, {"89", "sampleUsername", "2026-12-01"}}},
		{"testdata/get_accounts_string_and_number.json", [][3]string{{"1", "demotestingvpn", "2026-07-19"}, {"2", "sampleUsername", "1784419200"}, {"15", "later-user", "2026-08-01 00:00:00"}}},
		{"testdata/get_accounts_objects.json", [][3]string{{"15", "demotestingvpn", "2026-07-19"}, {"16", "sampleUsername", "2026-08-01"}}},
		{"testdata/get_accounts_php_map.json", [][3]string{{"1", "demotestingvpn", "2026-07-19"}, {"2", "later-user", "2026-08-01"}}},
		{"testdata/get_accounts_encoded.json", [][3]string{{"43", "demotestingvpn", "2026-07-19"}}},
	}
	for _, tc := range lists {
		t.Run(tc.file, func(t *testing.T) {
			c, _ := fixtureClient(t, tc.file)
			rows, err := c.GetAccounts(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(tc.want) {
				t.Fatalf("rows %+v", rows)
			}
			for i := range tc.want {
				if rows[i] != tc.want[i] {
					t.Fatalf("row %d %+v", i, rows)
				}
				for _, cell := range rows[i] {
					if strings.Contains(cell, "samplePassword") {
						t.Fatalf("password leaked into account row %+v", rows[i])
					}
				}
			}
		})
	}
}

func TestCredentialFieldVariants(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		user string
		pass string
		err  string
	}{
		{"case folded", map[string]any{"status": "success", "Username": "sampleUsername", "Password": "samplePassword"}, "sampleUsername", "samplePassword", ""},
		{"mixed pair", map[string]any{"result": "success", "vpnUsername": "sampleUsername", "password": "samplePassword"}, "sampleUsername", "samplePassword", ""},
		{"numeric password", map[string]any{"status": "success", "username": "sampleUsername", "password": json.Number("48291")}, "sampleUsername", "48291", ""},
		{"password only", map[string]any{"status": "success", "password": "samplePassword"}, "", "", "缺少 vpnUsername"},
		{"user only", map[string]any{"status": "success", "username": "sampleUsername"}, "", "", "缺少 vpnPassword"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := jsonClient(t, tc.body)
			acc, err := c.GetTrial(context.Background(), "trial-user@users.invalid")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %+v %v", acc, err)
				}
				return
			}
			if err != nil || acc.Username != tc.user || acc.Password != tc.pass {
				t.Fatalf("got %+v %v", acc, err)
			}
		})
	}
}

func TestGetAccountsUseNumberAndPositionalObject(t *testing.T) {
	raw, err := os.ReadFile("testdata/get_accounts_documented.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	rows, err := parseAccounts(body["accounts"])
	if err != nil || len(rows) != 2 || rows[0] != [3]string{"1", "demotestingvpn", "2026-07-19"} || rows[1][0] != "89" {
		t.Fatalf("%+v %v", rows, err)
	}
	rows, err = parseAccounts(map[string]any{"0": json.Number("7"), "1": "demotestingvpn", "2": "2026-07-19"})
	if err != nil || len(rows) != 1 || rows[0] != [3]string{"7", "demotestingvpn", "2026-07-19"} {
		t.Fatalf("positional %+v %v", rows, err)
	}
	if _, err := parseAccounts([]any{[]any{"not-an-id", "demotestingvpn", "2026-07-19"}}); err == nil || !strings.Contains(err.Error(), "账号编号不对") {
		t.Fatal(err)
	}
}

func fixtureClient(t *testing.T, file string) (*Client, *string) {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return jsonClient(t, json.RawMessage(raw))
}

func jsonClient(t *testing.T, body any) (*Client, *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch doc := body.(type) {
		case json.RawMessage:
			_, _ = w.Write(doc)
		default:
			_ = json.NewEncoder(w).Encode(doc)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Settings{BaseURL: srv.URL, Username: "reseller-user", Password: "reseller-secret", Sandbox: true})
	return c, &got
}

func TestProductionWireGuardResponses(t *testing.T) {
	cfg := "[Interface]\nPrivateKey = " + testKey(3) + "\nAddress = 10.8.0.2/24\nDNS = 1.1.1.1, 8.8.8.8\n\n[Peer]\nPublicKey = " + testKey(4) + "\nEndpoint = 203.0.113.10:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"
	var gotGet string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		switch strings.TrimPrefix(r.URL.Path, "/") {
		case "addWireGuardPeer":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": "success", "action": "addWireGuardPeer", "config_id": 17,
			})
		case "getWireGuardConfig":
			gotGet = body
			if !strings.Contains(body, "wireguardId=17") {
				_ = json.NewEncoder(w).Encode(map[string]any{"result": "error", "message": "Missing required parameters"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": "success", "action": "getWireGuardConfig", "wireguard_config": cfg,
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"result": "error", "action": "addWireGuardPeer",
				"message": "You have reached the maximum limit of 1 WireGuard configurations for your account type (dialup).",
			})
		}
	}))
	defer srv.Close()
	c := NewClient(Settings{BaseURL: srv.URL, Username: "reseller-user", Password: "reseller-secret", Sandbox: true})
	peer, err := c.AddWireGuardPeer(context.Background(), "trial-user", 43)
	if err != nil || peer.WireGuardID != "17" || peer.ClientIP != "" {
		t.Fatalf("add %+v %v", peer, err)
	}
	m, err := c.GetWireGuardConfig(context.Background(), "trial-user", peer.WireGuardID)
	if err != nil || m.Address != "10.8.0.2/32" || m.MTU != 1280 || m.Endpoint != "203.0.113.10:51820" || len(m.DNS) != 2 {
		t.Fatalf("config %+v %v body=%s", m, err, gotGet)
	}
	if _, err := c.GetWireGuardConfig(context.Background(), "trial-user", ""); err == nil || !strings.Contains(err.Error(), "Missing required parameters") {
		t.Fatalf("missing id: %v", err)
	}
	c.BaseURL = srv.URL
	limitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result":  "error",
			"message": "You have reached the maximum limit of 1 WireGuard configurations for your account type (dialup).",
		})
	}))
	defer limitSrv.Close()
	c.BaseURL = limitSrv.URL
	_, err = c.AddWireGuardPeer(context.Background(), "trial-user", 43)
	n, ok := upstreamPeerLimit(err)
	if !ok || n != 1 || !strings.Contains(err.Error(), "maximum limit of 1") {
		t.Fatalf("limit: %v %d %v", err, n, ok)
	}
}

func TestSweepConfigs(t *testing.T) {
	api := &fakeAPI{foundIDs: map[string]bool{"2": true, "5": true}, material: wireGuardMaterial{Address: "10.8.0.2/32"}}
	hits, err := SweepConfigs(context.Background(), api, "trial-user", 1, 6, false, true)
	if err != nil || len(hits) != 2 || hits[0].ID != "2" || hits[1].ID != "5" || hits[0].Deleted || api.peerDels != 0 {
		t.Fatalf("dry-run %+v dels=%d %v", hits, api.peerDels, err)
	}
	hits, err = SweepConfigs(context.Background(), api, "trial-user", 1, 6, true, true)
	if err != nil || len(hits) != 2 || !hits[0].Deleted || !hits[1].Deleted || len(api.deletedPeers) != 2 {
		t.Fatalf("delete %+v peers=%v %v", hits, api.deletedPeers, err)
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
