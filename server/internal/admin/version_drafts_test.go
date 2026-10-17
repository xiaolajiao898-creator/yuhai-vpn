package admin

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"testing"
)

func validDraftInput() VersionCreate {
	cert := strings.Repeat("b", 64)
	return VersionCreate{OperationID: uuid.NewString(), VersionDraftFields: VersionDraftFields{Platform: "ANDROID", VersionName: "1.2.3", VersionCode: "123", Minimum: "100", DownloadURL: "https://download.example.com/app.apk", SHA256: strings.Repeat("a", 64), CertSHA256: &cert, Notes: "安全更新\n改进连接"}}
}
func TestVersionDraftValidation(t *testing.T) {
	if !validDraftInput().valid() {
		t.Fatal("valid draft rejected")
	}
	for name, change := range map[string]func(*VersionCreate){
		"non-v4":         func(v *VersionCreate) { v.OperationID = "00000000-0000-1000-8000-000000000000" },
		"uppercase UUID": func(v *VersionCreate) { v.OperationID = "A6C37C83-2426-4F7E-8A67-64D5AE421490" },
		"zero code":      func(v *VersionCreate) { v.VersionCode = "0" }, "leading zero": func(v *VersionCreate) { v.VersionCode = "0123" }, "overflow": func(v *VersionCreate) { v.VersionCode = "2147483648" },
		"minimum above code": func(v *VersionCreate) { v.Minimum = "124" }, "negative minimum": func(v *VersionCreate) { v.Minimum = "-1" },
		"missing cert": func(v *VersionCreate) { v.CertSHA256 = nil }, "missing hash": func(v *VersionCreate) { v.SHA256 = "" }, "uppercase hash": func(v *VersionCreate) { v.SHA256 = strings.Repeat("A", 64) },
		"local link": func(v *VersionCreate) { v.DownloadURL = "https://127.0.0.1/app.apk" }, "credential link": func(v *VersionCreate) { v.DownloadURL = "https://user:password@example.com/a" }, "query token": func(v *VersionCreate) { v.DownloadURL += "?token=value" },
		"null note": func(v *VersionCreate) { v.Notes = "bad\x00note" }, "large notes": func(v *VersionCreate) { v.Notes = strings.Repeat("界", 5400) }, "control version": func(v *VersionCreate) { v.VersionName = "1.\n2" },
	} {
		t.Run(name, func(t *testing.T) {
			v := validDraftInput()
			change(&v)
			if v.valid() {
				t.Fatal("invalid draft accepted")
			}
		})
	}
	v := validDraftInput()
	v.Platform = "IOS"
	v.CertSHA256 = nil
	if !v.valid() {
		t.Fatal("iOS nullable certificate rejected")
	}
}
func TestVersionDraftStrictBodyAndDetailShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	in := validDraftInput()
	raw, _ := json.Marshal(in)
	for _, body := range []string{string(raw[:len(raw)-1]) + `,"operation_id":"duplicate"}`, string(raw[:len(raw)-1]) + `,"status":"PUBLISHED"}`, `[]`, `null`, strings.Replace(string(raw), `"release_notes":"安全更新\n改进连接"`, `"release_notes":null`, 1), strings.Replace(string(raw), `"cert_sha256":"`+*in.CertSHA256+`",`, "", 1), strings.Repeat(" ", 24*1024) + string(raw)} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "http://admin.example/admin/v1/app-versions", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		var out VersionCreate
		if _, err := versionDraftBody(c, &out, false); err == nil {
			t.Fatal("bad body accepted")
		}
	}
	detail := VersionDetail{versionSnapshot: in.snapshot("7")}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil || fields["id"] != "7" || fields["version_code"] != "123" || fields["status"] != "DRAFT" {
		t.Fatal("detail fields not flattened")
	}
	if _, ok := fields["operation_id"]; ok {
		t.Fatal("operation field leaked in detail")
	}
	for _, f := range []VersionFilter{{PageSize: 51}, {Platform: "OTHER"}, {VersionCode: "123"}, {Platform: "IOS", VersionCode: "0001"}, {Status: "PENDING"}} {
		if _, err := normalizeVersionFilter(f); err == nil {
			t.Fatal("bad filter accepted")
		}
	}
}
