package account

import "testing"

func TestDeviceSignInPlan(t *testing.T) {
	cases := []struct {
		name                    string
		found, sameUser, active bool
		count, limit            int
		want                    string
	}{
		{"new under cap", false, false, false, 1, 3, "create"},
		{"new at cap", false, false, false, 3, 3, "limit"},
		{"uid belongs to someone else", true, false, true, 1, 3, "conflict"},
		{"same active phone", true, true, true, 3, 3, "resume"},
		{"revoked phone with room", true, true, false, 1, 3, "reactivate"},
		{"revoked phone at cap", true, true, false, 3, 3, "limit"},
	}
	for _, c := range cases {
		if got := deviceSignInPlan(c.found, c.sameUser, c.active, c.count, c.limit); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
