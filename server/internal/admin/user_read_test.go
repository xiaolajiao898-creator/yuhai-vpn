package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestManagedUserQueryBoundaries(t *testing.T) {
	for _, f := range []ManagedUserFilter{{Page: -1}, {Page: 10001}, {PageSize: 51}, {PageSize: -1}, {AccountNo: "0123456789"}, {AccountNo: "123"}, {AccountNo: "1234567890 OR 1=1"}, {AccountNo: "１２３４５６７８９０"}, {Status: "LOCKED"}, {Status: "ACTIVE "}} {
		if _, err := normalizeManagedUserFilter(f); err == nil {
			t.Fatal("invalid user filter accepted", f)
		}
	}
	good, err := normalizeManagedUserFilter(ManagedUserFilter{AccountNo: "1234567890", Status: "ACTIVE"})
	if err != nil || good.Page != 1 || good.PageSize != 20 {
		t.Fatal(good, err)
	}
	if _, _, err = normalizeUserPage(10000, 50); err != nil {
		t.Fatal("allowed page rejected")
	}
}
func TestManagedUserDTOContainsOnlyAllowlistedBusinessFields(t *testing.T) {
	detail := ManagedUserDetail{ManagedUserSummary: ManagedUserSummary{ID: "9007199254740993", AccountNo: "1234567890", Currency: "USDT"}, InviteCode: "ABC234"}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	expected := []string{"id", "account_no", "status", "balance", "currency", "active_device_count", "created_at", "updated_at", "invite_code", "balance_updated_at", "subscription"}
	if len(fields) != len(expected) {
		t.Fatal("unexpected detail fields", string(raw))
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing field", key)
		}
	}
	if fields["id"] != "9007199254740993" || fields["balance"] != nil || fields["subscription"] != nil {
		t.Fatal("precision/null contract changed")
	}
	for _, value := range []any{ManagedBalanceEntry{}, ManagedEntitlementEntry{}, ManagedSubscription{}} {
		raw, _ := json.Marshal(value)
		for _, forbidden := range []string{"secret", "hash", "token", "cookie", "remark", "snapshot", "idempotency_key", "operator_id", "pubkey"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("unexpected protected field", forbidden)
			}
		}
	}
}
