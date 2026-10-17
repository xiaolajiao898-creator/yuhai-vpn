package node

import (
	"strings"
	"testing"
	"time"
)

func readyCandidate(now time.Time) candidate {
	pin := strings.Repeat("a", 64)
	checksum := strings.Repeat("b", 64)
	version := int64(1)
	return candidate{view: View{Name: "Hong Kong", CountryCode: "HK", City: "Hong Kong", Region: "ASIA"}, id: 9007199254740993, host: "node.invalid", port: 443, status: "ONLINE", load: 30, minTier: 1, lastHeartbeat: &now, received: &now, sentAt: &now, pin: &pin, receiptPin: &pin, checksum: &checksum, configVersion: &version, protocols: []string{"SS2022", "WIREGUARD"}, readyProtocols: []string{"SHADOWSOCKS_2022", "WIREGUARD"}}
}
func TestCandidateEligibilityAndNoMockFallback(t *testing.T) {
	now := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, reason string
		change       func(*candidate)
	}{
		{"ready", "", func(n *candidate) {}}, {"maintenance", "MAINTENANCE", func(n *candidate) { n.status = "MAINTENANCE" }},
		{"missing heartbeat", "OFFLINE", func(n *candidate) { n.lastHeartbeat = nil }}, {"stale", "OFFLINE", func(n *candidate) { v := now.Add(-90*time.Second - time.Microsecond); n.lastHeartbeat = &v }}, {"future", "OFFLINE", func(n *candidate) { v := now.Add(time.Microsecond); n.lastHeartbeat = &v }},
		{"late delivery", "OFFLINE", func(n *candidate) { v := now.Add(-91 * time.Second); n.sentAt = &v }}, {"missing sample time", "NOT_READY", func(n *candidate) { n.sentAt = nil }},
		{"no certificate", "NOT_READY", func(n *candidate) { n.pin = nil }}, {"old certificate", "NOT_READY", func(n *candidate) { v := strings.Repeat("c", 64); n.pin = &v }}, {"old receipt", "NOT_READY", func(n *candidate) { n.received = nil }}, {"old migration", "NOT_READY", func(n *candidate) { n.receiptPin = nil }},
		{"no configuration", "NOT_READY", func(n *candidate) { v := int64(0); n.configVersion = &v }}, {"no snapshot", "NOT_READY", func(n *candidate) { v := ""; n.checksum = &v }}, {"no inbound", "NOT_READY", func(n *candidate) { n.protocols = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := readyCandidate(now)
			tc.change(&n)
			v, err := project(n, 1, now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.reason == "" {
				if !v.Available || v.Protocols[0] != "SHADOWSOCKS_2022" || v.ID != "9007199254740993" {
					t.Fatal(v)
				}
			} else if v.Available || v.LockedReason == nil || *v.LockedReason != tc.reason || v.Probe != nil || v.Load != nil || len(v.Protocols) != 0 {
				t.Fatal("unready metadata escaped", v)
			}
		})
	}
	n := readyCandidate(now)
	if v, err := project(n, 0, now); err != nil || *v.LockedReason != "TIER" || v.Probe != nil {
		t.Fatal(v, err)
	}
	n.protocols = []string{"MOCK"}
	if _, err := project(n, 1, now); err == nil {
		t.Fatal("MOCK accepted in real API")
	}
	n = readyCandidate(now.Add(-90 * time.Second))
	if v, err := project(n, 1, now); err != nil || !v.Available {
		t.Fatal("90 second boundary", v, err)
	}
}
func TestNodeETagCoversVisibleFieldsAndExpiry(t *testing.T) {
	now := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	n := readyCandidate(now)
	v, _ := project(n, 1, now)
	first, _ := listVersion([]View{v})
	for _, modify := range []func(*candidate){func(n *candidate) { n.view.Name = "changed" }, func(n *candidate) { n.host = "changed.invalid" }, func(n *candidate) { n.protocols = []string{"TROJAN"} }, func(n *candidate) { n.view.IsRecommended = true }} {
		other := n
		modify(&other)
		view, _ := project(other, 1, now)
		tag, _ := listVersion([]View{view})
		if tag == first {
			t.Fatal("visible change did not invalidate ETag")
		}
	}
	old, _ := project(n, 1, now.Add(90*time.Second+time.Microsecond))
	tag, _ := listVersion([]View{old})
	if tag == first {
		t.Fatal("expired heartbeat still 304")
	}
	same, _ := project(n, 1, now.Add(time.Second))
	tag, _ = listVersion([]View{same})
	if tag != first {
		t.Fatal("unchanged visible response changed ETag")
	}
}

func TestCandidateOnlyReturnsObservedProtocols(t *testing.T) {
	now := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	n := readyCandidate(now)
	n.protocols = append(n.protocols, "WIREGUARD")
	n.readyProtocols = []string{"WIREGUARD"}
	v, e := project(n, 1, now)
	if e != nil || !v.Available || strings.Join(v.Protocols, ",") != "WIREGUARD" {
		t.Fatal("unobserved protocol exposed", v, e)
	}
	for _, ready := range [][]string{nil, {}, {"TROJAN"}} {
		n.readyProtocols = ready
		v, e = project(n, 1, now)
		if e != nil || v.Available || *v.LockedReason != "NOT_READY" || v.Probe != nil {
			t.Fatal(v, e)
		}
	}
	for _, ready := range [][]string{{"MOCK"}, {"WIREGUARD", "WIREGUARD"}} {
		n.readyProtocols = ready
		if _, e = project(n, 1, now); e == nil {
			t.Fatal("invalid ready set accepted")
		}
	}
}
