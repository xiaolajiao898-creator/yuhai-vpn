package money

import "testing"

func TestParse(t *testing.T) {
	ok := map[string]Micro{
		"100.0043": 100_004_300, "0.1": 100_000, "50": 50_000_000, "167.604300": 167_604_300, "0.000001": 1,
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "-1", "1e3", "1,000", "1.0000001", ".5", "abc", "1.2.3", "+1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) 应失败", bad)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	m, _ := Parse("167.6043")
	if m.String() != "167.604300" || m.Display() != "167.6043" {
		t.Fatalf("String=%s Display=%s", m.String(), m.Display())
	}
	if Micro(3_200_000).Display() != "3.2000" {
		t.Fatalf("Display 至少保留 4 位小数: %s", Micro(3_200_000).Display())
	}
}

func TestCeilAndTail(t *testing.T) {
	if CeilToTenth(167_560_000) != 167_600_000 || CeilToTenth(100_000) != 100_000 {
		t.Fatal("CeilToTenth")
	}
	for i := 0; i < 1000; i++ {
		tail, err := RandomTail()
		if err != nil || tail < 100 || tail > 99_900 || tail%100 != 0 {
			t.Fatalf("RandomTail=%d err=%v", tail, err)
		}
	}
	b, tl := SplitTail(167_604_300)
	if b != 167_600_000 || tl != 4_300 {
		t.Fatalf("SplitTail=%d,%d", b, tl)
	}
}
