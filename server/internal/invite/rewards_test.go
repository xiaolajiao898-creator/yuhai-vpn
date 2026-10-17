package invite

import (
	"context"
	"math"
	"testing"

	"github.com/yuhai-vpn/server/internal/outbox"
)

func TestRewardCalculationIsIntegerExactAndRejectsAmbiguousRules(t *testing.T) {
	bp := 500
	r := rewardRule{Version: 1, Beneficiary: "INVITER", Kind: "BALANCE", Percent: &bp}
	if v, ok := calculateReward(r, 10000001); !ok || v != 500000 {
		t.Fatal("percentage must round down in integer micro units", v, ok)
	}
	bp = 10000
	if v, ok := calculateReward(r, math.MaxInt64); !ok || v != math.MaxInt64 {
		t.Fatal("percentage intermediate overflow", v, ok)
	}
	r.Value = 1
	if _, ok := calculateReward(r, 1000000); ok {
		t.Fatal("ambiguous fixed plus percentage rule accepted")
	}
	r.Percent = nil
	r.Kind = "DAYS"
	r.Value = 36501
	if _, ok := calculateReward(r, 1); ok {
		t.Fatal("duration overflow accepted")
	}
	r.Kind = "TRAFFIC"
	r.Value = 1 << 53
	if _, ok := calculateReward(r, 1); ok {
		t.Fatal("unsafe traffic integer accepted")
	}
	r.Value = 1
	r.Kind = "UNKNOWN"
	if _, ok := calculateReward(r, 1); ok {
		t.Fatal("unknown reward type accepted")
	}
	s := &RewardService{}
	for _, e := range []outbox.Event{{}, {Type: outbox.OrderCompleted, AggregateType: "user", AggregateID: 1}, {Type: outbox.OrderCompleted, AggregateType: "order", AggregateID: -1}} {
		if err := s.OnOrderCompleted(context.Background(), e); err == nil {
			t.Fatal("invalid event reached database")
		}
	}
}
