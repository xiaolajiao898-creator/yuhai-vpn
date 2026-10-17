package money

import (
	"math"
	"testing"
)

func TestStringIntegerBounds(t *testing.T) {
	for input, want := range map[Micro]string{Micro(math.MinInt64): "-9223372036854.775808", Micro(math.MaxInt64): "9223372036854.775807", -1: "-0.000001", 0: "0.000000"} {
		if got := input.String(); got != want {
			t.Fatalf("%d: %s, want %s", input, got, want)
		}
	}
}
