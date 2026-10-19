package nodecontrol

import "testing"

func TestLoadScoreTreatsZeroCapacityAsCPUOnly(t *testing.T) {
	if loadScore(10, 0, 30) != 30 || loadScore(0, 0, 0) != 0 {
		t.Fatal("zero capacity must not divide")
	}
	if loadScore(501, 500, 12) != 100 {
		t.Fatal("connection ratio caps at 100")
	}
	if loadScore(1, 500, 40) != 40 {
		t.Fatal("cpu wins when it is higher than the connection ratio")
	}
	if loadScore(250, 500, 10) != 50 {
		t.Fatal("half full is 50")
	}
}
