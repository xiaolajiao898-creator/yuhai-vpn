package httpx

import (
	"reflect"
	"testing"
)

func TestTrustedProxiesFromEnv(t *testing.T) {
	cases := map[string][]string{
		"":                         nil,
		"127.0.0.1,::1":            {"127.0.0.1", "::1"},
		" 127.0.0.1 , 10.0.0.0/8 ": {"127.0.0.1", "10.0.0.0/8"},
		"0.0.0.0/0,::/0,127.0.0.1": {"127.0.0.1"},
		"bogus,127.0.0.1":          {"127.0.0.1"},
	}
	for in, want := range cases {
		t.Setenv("TRUSTED_PROXIES", in)
		if got := TrustedProxiesFromEnv(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: got %v want %v", in, got, want)
		}
	}
}
