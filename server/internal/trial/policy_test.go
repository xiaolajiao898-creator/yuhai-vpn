package trial

import "testing"

func TestParseInstallID(t *testing.T) {
	id, err := ParseInstallID("A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11")
	if err != nil || id.String() != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
		t.Fatalf("got %v %v", id, err)
	}
	for _, bad := range []string{"", "  ", "not-a-uuid", "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}", "00000000-0000-0000-0000-000000000000"} {
		if _, err := ParseInstallID(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestEnabled(t *testing.T) {
	t.Setenv("YUHAI_TRIAL_ENABLED", "")
	if !Enabled(nil, true) {
		t.Fatal("missing config should stay on")
	}
	if !Enabled([]byte("true"), false) {
		t.Fatal("true")
	}
	if Enabled([]byte("false"), false) {
		t.Fatal("false")
	}
	if Enabled([]byte(`"true"`), false) {
		t.Fatal("string true must not enable")
	}
	t.Setenv("YUHAI_TRIAL_ENABLED", "0")
	if Enabled([]byte("true"), false) {
		t.Fatal("env off must win")
	}
}
