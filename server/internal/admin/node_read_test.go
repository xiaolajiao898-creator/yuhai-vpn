package admin

import (
	"testing"
	"time"
)

func TestManagedNodeFilterAndHeartbeatBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		age   time.Duration
		state string
	}{{0, "FRESH"}, {90 * time.Second, "FRESH"}, {90*time.Second + time.Nanosecond, "STALE"}, {-time.Nanosecond, "CLOCK_SKEW"}} {
		last := now.Add(-c.age)
		if got := nodeHeartbeat(&last, now); got != c.state {
			t.Fatalf("age %v: %s", c.age, got)
		}
	}
	if nodeHeartbeat(nil, now) != "NEVER" {
		t.Fatal("missing heartbeat became healthy")
	}
	for _, f := range []ManagedNodeFilter{{Page: -1}, {Page: 10001}, {PageSize: 51}, {Search: " "}, {Search: "name\n"}, {Search: " name"}, {Status: "online"}, {CountryCode: "hk"}, {CountryCode: "HKG"}} {
		if _, err := normalizeManagedNodeFilter(f); err == nil {
			t.Fatal("invalid filter accepted", f)
		}
	}
	f, err := normalizeManagedNodeFilter(ManagedNodeFilter{Search: "线路_%", CountryCode: "HK", Status: "ONLINE"})
	if err != nil || f.Page != 1 || f.PageSize != 20 || f.Search != "线路_%" {
		t.Fatal("literal search/defaults", f, err)
	}
}
