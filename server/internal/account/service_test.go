package account

import (
	"testing"
	"time"
)

func TestConnectability(t *testing.T) {
	now := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	future, past := now.Add(24*time.Hour), now.Add(-time.Hour)
	quota := int64(1000)
	cases := []struct {
		name   string
		status string
		e      entitlementRow
		packs  []TrafficPack
		ok     bool
		reason string
	}{
		{"无套餐", "ACTIVE", entitlementRow{}, nil, false, "NO_PLAN"},
		{"有效时长", "ACTIVE", entitlementRow{Tier: 1, ExpireAt: &future}, nil, true, ""},
		{"已过期", "ACTIVE", entitlementRow{Tier: 1, ExpireAt: &past}, nil, false, "EXPIRED"},
		{"过期但有流量包", "ACTIVE", entitlementRow{Tier: 1, ExpireAt: &past}, []TrafficPack{{RemainingBytes: 1}}, true, ""},
		{"周期配额用完", "ACTIVE", entitlementRow{Tier: 1, ExpireAt: &future, PeriodQuotaBytes: &quota, PeriodUsedBytes: 1000}, nil, false, "QUOTA_EXHAUSTED"},
		{"停用", "SUSPENDED", entitlementRow{Tier: 1, ExpireAt: &future}, nil, false, "SUSPENDED"},
	}
	for _, c := range cases {
		ok, reason := connectability(c.status, c.e, c.packs, now)
		got := ""
		if reason != nil {
			got = *reason
		}
		if ok != c.ok || got != c.reason {
			t.Errorf("%s: got (%v,%q) want (%v,%q)", c.name, ok, got, c.ok, c.reason)
		}
	}
}

func TestIPPrefix(t *testing.T) {
	if ipPrefix("203.0.113.42") != "203.0.113.0/24" {
		t.Fatal(ipPrefix("203.0.113.42"))
	}
	if ipPrefix("2001:db8:abcd:1::5") != "2001:db8:abcd::/48" {
		t.Fatal(ipPrefix("2001:db8:abcd:1::5"))
	}
}
