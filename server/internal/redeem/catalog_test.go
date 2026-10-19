package redeem

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationSeedsOwnerTerms(t *testing.T) {
	raw, err := os.ReadFile("../db/migrations/00042_redeem_codes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	for _, term := range SeedTerms() {
		needle := "('" + term.Code + "','" + term.Name + "'," + strconv.Itoa(term.Days) + "," + strconv.Itoa(term.Tier) + "," + strconv.Itoa(term.Devices) + ",NULL)"
		if !strings.Contains(up, needle) {
			t.Fatalf("migration missing seed %s", needle)
		}
	}
	if strings.Count(up, "INSERT INTO redeem_card_types") != 1 {
		t.Fatal("card terms must be seeded in one place")
	}
}
