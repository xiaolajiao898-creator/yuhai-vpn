package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestManagedDeviceFilterAndMetadataBoundaries(t *testing.T) {
	for _, f := range []ManagedDeviceFilter{{Page: -1}, {Page: 10001}, {PageSize: -1}, {PageSize: 51}, {Status: "SUSPENDED"}, {Status: "active"}, {Status: "ACTIVE "}, {Status: "REVOKED OR 1=1"}} {
		if _, err := normalizeManagedDeviceFilter(f); err == nil {
			t.Fatal("invalid filter accepted", f)
		}
	}
	for _, status := range []string{"", "ACTIVE", "REVOKED"} {
		f, err := normalizeManagedDeviceFilter(ManagedDeviceFilter{Status: status})
		if err != nil || f.Page != 1 || f.PageSize != 20 || f.Status != status {
			t.Fatal("defaults or status", f, err)
		}
	}
	if _, err := normalizeManagedDeviceFilter(ManagedDeviceFilter{Page: 10000, PageSize: 50}); err != nil {
		t.Fatal("valid maximum rejected", err)
	}
	base := ManagedDevice{ID: "9007199254740993", Platform: "ANDROID", Status: "ACTIVE", CreatedAt: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	if err := validateManagedDevice(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "<b>设备名</b>", strings.Repeat("宇", 64)} {
		d := base
		d.DisplayName = &name
		if err := validateManagedDevice(d); err != nil {
			t.Fatal("valid plain text rejected", err)
		}
	}
	for _, name := range []string{strings.Repeat("宇", 65), "设备\n名称", "device\u007f", "device\u0085", string([]byte{0xff})} {
		d := base
		d.DisplayName = &name
		if err := validateManagedDevice(d); err == nil {
			t.Fatal("invalid display name accepted")
		}
	}
	for _, mutate := range []func(*ManagedDevice){func(d *ManagedDevice) { d.ID = "01" }, func(d *ManagedDevice) { d.ID = "9223372036854775808" }, func(d *ManagedDevice) { d.Platform = "WEB" }, func(d *ManagedDevice) { d.Status = "ONLINE" }, func(d *ManagedDevice) { d.RevokedAt = &base.CreatedAt }} {
		d := base
		mutate(&d)
		if err := validateManagedDevice(d); err == nil {
			t.Fatal("invalid metadata accepted", d)
		}
	}
	// Legacy revocations without a timestamp and a late touch are both valid.
	revoked := base
	revoked.Status = "REVOKED"
	if err := validateManagedDevice(revoked); err != nil {
		t.Fatal("missing historical timestamp rejected", err)
	}
	later := base.CreatedAt.Add(time.Minute)
	revoked.RevokedAt, revoked.LastSeenAt = &base.CreatedAt, &later
	if err := validateManagedDevice(revoked); err != nil {
		t.Fatal("concurrent late touch rejected", err)
	}
}

func TestManagedDeviceDTOFieldsAndNulls(t *testing.T) {
	out := ManagedDevicePage{UserID: "9007199254740993", Items: []ManagedDevice{{ID: "9223372036854775807", Platform: "IOS", Status: "REVOKED"}}, Total: "1", Page: 1, PageSize: 20}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		UserID string           `json:"user_id"`
		Items  []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	item := decoded.Items[0]
	keys := []string{"id", "platform", "display_name", "status", "created_at", "last_seen_at", "revoked_at"}
	if len(item) != len(keys) || decoded.UserID != "9007199254740993" || item["id"] != "9223372036854775807" {
		t.Fatal("precision or field projection changed", string(raw))
	}
	for _, key := range keys {
		if _, ok := item[key]; !ok {
			t.Fatal("missing field", key)
		}
	}
	for _, key := range []string{"display_name", "last_seen_at", "revoked_at"} {
		if item[key] != nil {
			t.Fatal("missing value was invented", key)
		}
	}
	for _, forbidden := range []string{"device_uid", "user_id\":9007", "pubkey", "fingerprint", "token", "secret", "model", "os_version", "app_version", "risk", "ip_hash"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("protected metadata exposed", forbidden)
		}
	}
}
