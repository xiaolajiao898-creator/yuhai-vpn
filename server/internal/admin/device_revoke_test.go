package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDeviceRevokeInputAndExactRequestBinding(t *testing.T) {
	good := DeviceRevokeInput{OperationID: uuid.NewString(), UserID: "9007199254740993", Reason: "设备遗失，撤销访问"}
	if !good.valid() {
		t.Fatal("valid input rejected")
	}
	for _, change := range []func(*DeviceRevokeInput){func(v *DeviceRevokeInput) { v.UserID = "01" }, func(v *DeviceRevokeInput) { v.UserID = "9223372036854775808" }, func(v *DeviceRevokeInput) { v.OperationID = uuid.Nil.String() }, func(v *DeviceRevokeInput) { v.OperationID = strings.ToUpper(v.OperationID) }, func(v *DeviceRevokeInput) { v.Reason = " " }, func(v *DeviceRevokeInput) { v.Reason = "bad\nreason" }, func(v *DeviceRevokeInput) { v.Reason = strings.Repeat("界", 501) }} {
		v := good
		change(&v)
		if v.valid() {
			t.Fatal("bad input accepted", v)
		}
	}
	raw, _ := json.Marshal(good)
	path := "/admin/v1/devices/7"
	hash := RequestHash("DELETE", path, raw)
	if hash == RequestHash("DELETE", "/admin/v1/devices/8", raw) || hash == RequestHash("POST", path, raw) || hash == RequestHash("DELETE", path, append(raw, ' ')) {
		t.Fatal("request not bound exactly")
	}
	for _, raw := range []string{`{"operation_id":"a","operation_id":"b"}`, `{"unknown":"field"}`, `{"user_id":12}`, `{} {}`, `[]`} {
		var in DeviceRevokeInput
		if decodeApprovalObject([]byte(raw), &in) == nil && in.valid() {
			t.Fatal("unsafe payload accepted")
		}
	}
}
