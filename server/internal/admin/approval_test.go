package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func sampleVersionSnapshot() versionSnapshot {
	link, hash := "https://download.example.com/app.apk", strings.Repeat("a", 64)
	return versionSnapshot{ID: "1", Platform: "ANDROID", VersionName: "1.2.3", VersionCode: "12", Minimum: "1", DownloadURL: &link, SHA256: &hash, CertSHA256: &hash, Status: "DRAFT"}
}
func TestApprovalSnapshotValidationAndCanonicalHash(t *testing.T) {
	good := sampleVersionSnapshot()
	if !good.valid() {
		t.Fatal("valid version rejected")
	}
	decoded, err := decodeVersionSnapshot(good.bytes())
	if err != nil || decoded.hash() != good.hash() {
		t.Fatal(err)
	}
	for _, edit := range []func(*versionSnapshot){
		func(v *versionSnapshot) { v.Status = "PUBLISHED" }, func(v *versionSnapshot) { v.ID = "01" }, func(v *versionSnapshot) { v.Platform = "WINDOWS" },
		func(v *versionSnapshot) { v.VersionCode = "0" }, func(v *versionSnapshot) { v.VersionCode = "2147483648" }, func(v *versionSnapshot) { v.Minimum = "13" },
		func(v *versionSnapshot) { v.SHA256 = nil }, func(v *versionSnapshot) { v.CertSHA256 = nil }, func(v *versionSnapshot) { v.DownloadURL = nil },
		func(v *versionSnapshot) { v.VersionName = "bad\nname" },
	} {
		v := good
		edit(&v)
		if v.valid() {
			t.Fatalf("invalid snapshot accepted: %+v", v)
		}
	}
	fieldMap := good.fields()
	if *fieldMap["version_code"] != "12" || fieldMap["published_at"] != nil {
		t.Fatal("snapshot fields must be string/null", fieldMap)
	}
	bad := strings.TrimSuffix(string(good.bytes()), "}") + `,"secret":"must not be returned"}`
	a := Approval{ActionCode: publishAction, TargetType: "app_version", TargetID: &good.ID, PayloadHash: good.hash(), payload: []byte(bad)}
	a.present(time.Now(), true)
	raw, _ := json.Marshal(a)
	if a.Snapshot != nil || strings.Contains(string(raw), "must not") {
		t.Fatal("unsafe payload exposed", string(raw))
	}
	a = Approval{ActionCode: "funds.adjust", TargetType: "balance", payload: good.bytes()}
	a.present(time.Now(), true)
	if a.ExecutorSupported || a.Snapshot != nil {
		t.Fatal("unknown executor advertised")
	}
}
func TestApprovalDownloadDestinationSafety(t *testing.T) {
	for _, link := range []string{"https://download.example.com/app.apk", "https://testflight.apple.com/join/example", "https://downloads.example.com:8443/app.apk"} {
		if !safePublicDownload(link) {
			t.Fatal("safe URL rejected", link)
		}
	}
	for _, link := range []string{"http://download.example.com/app.apk", "https://user:pass@example.com/app.apk", "https://127.0.0.1/app.apk", "https://198.18.0.1/app.apk", "https://192.0.2.1/app.apk", "https://0.1.2.3/app.apk", "https://8.8.8.8/app.apk", "https://[2001:4860:4860::8888]/app.apk", "https://10.1.1.1/app.apk", "https://[::1]/app.apk", "https://[::ffff:127.0.0.1]/app.apk", "https://0x7f.0.0.1/app.apk", "https://0x7f.1/app.apk", "https://0177.0.0.1/app.apk", "https://2130706433/app.apk", "https://localhost/app.apk", "https://service.internal/app.apk", "https://example.com/app.apk?token=secret", "https://example.com/app.apk#other", "https://example.com:65536/app.apk", "https://example.com\\@127.0.0.1/app.apk"} {
		if safePublicDownload(link) {
			t.Fatal("unsafe URL accepted", link)
		}
	}
}
func TestApprovalStrictBodiesAndPagination(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{}`, `{"reason":"ok","extra":true}`, `{"reason":"first","reason":"second"}`, `{"reason":42}`, `{"reason":"ok"} {}`, strings.Repeat("x", 5000)} {
		var in struct {
			Reason string `json:"reason"`
		}
		err := decodeApprovalObject([]byte(body), &in)
		if body == `{}` {
			if err != nil {
				t.Fatal(err)
			}
			if approvalText(in.Reason) {
				t.Fatal("missing reason accepted")
			}
			continue
		}
		if err == nil {
			t.Fatal("invalid body accepted", body)
		}
	}
	for _, value := range []string{"", " padded", "line\nbreak", strings.Repeat("字", 501)} {
		if approvalText(value) {
			t.Fatal("unsafe text accepted")
		}
	}
	r := gin.New()
	r.POST("/test", func(c *gin.Context) {
		var input struct{}
		if _, err := approvalBody(c, &input); err != nil {
			c.Status(400)
			return
		}
		c.Status(200)
	})
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{`+strings.Repeat(" ", 4096)+`}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
	for _, f := range []ApprovalFilter{{Page: -1}, {PageSize: 51}, {Scope: "other"}, {Status: "INVALID"}} {
		if _, err := normalizeApprovalFilter(f); err == nil {
			t.Fatal("invalid filter accepted", f)
		}
	}
}
