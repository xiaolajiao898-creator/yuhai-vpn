package worldvpn

import (
	"os"
	"strings"
	"testing"
)

// 生产库已经有 00042 的 redeem_codes（duration_days、批次、unused/used/revoked）。
// 46 只能追加批发自己的表，不能再 CREATE 或改写那张表。
func TestForwardMigrationLeavesRedeemSchemaIntact(t *testing.T) {
	redeem, err := os.ReadFile("../db/migrations/00042_redeem_codes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up42, _, _ := strings.Cut(string(redeem), "-- +goose Down")
	for _, needle := range []string{
		"CREATE TABLE redeem_codes",
		"duration_days",
		"CREATE TABLE redeem_batches",
		"CREATE TABLE redeem_card_types",
		"status IN ('unused','used','revoked')",
		"batch_id",
		"card_type",
	} {
		if !strings.Contains(up42, needle) {
			t.Fatalf("00042 lost %q", needle)
		}
	}
	if strings.Contains(up42, "period_months") || strings.Contains(up42, "'VOID'") || strings.Contains(up42, "'PENDING'") {
		t.Fatal("00042 picked up the wholesale redeem shape")
	}

	forward, err := os.ReadFile("../db/migrations/00046_worldvpn.sql")
	if err != nil {
		t.Fatal(err)
	}
	up46, _, _ := strings.Cut(string(forward), "-- +goose Down")
	sql46 := stripSQLComments(up46)
	if strings.Contains(strings.ToLower(sql46), "redeem_codes") || strings.Contains(sql46, "redeem_batches") || strings.Contains(sql46, "trial_grants") {
		t.Fatal("00046 must not touch redeem or trial tables")
	}
	for _, needle := range []string{
		"CREATE TABLE worldvpn_accounts",
		"CREATE TABLE worldvpn_grants",
		"CREATE TABLE worldvpn_peers",
		"CREATE TABLE worldvpn_state",
	} {
		if !strings.Contains(up46, needle) {
			t.Fatalf("00046 missing %q", needle)
		}
	}
	if strings.Contains(sql46, "CREATE TABLE redeem") {
		t.Fatal("00046 recreates a redeem table")
	}
	later, err := os.ReadFile("../db/migrations/00047_worldvpn_billing_memory.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql47 := stripSQLComments(strings.Split(string(later), "-- +goose Down")[0])
	if strings.Contains(strings.ToLower(sql47), "redeem_codes") || strings.Contains(sql47, "trial_grants") || strings.Contains(sql47, "subscription_ledger") {
		t.Fatal("00047 must not touch redeem, trial, or payment ledger tables")
	}
}

func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
