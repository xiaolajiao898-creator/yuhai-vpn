package clientdiag

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRedactDropsWireGuardKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes32(7))
	if len(key) != 44 {
		t.Fatalf("fixture key length %d", len(key))
	}
	text := "PrivateKey = " + key + "\nservice=STOPPED\nendpoint=vpn.example.com:51820\n"
	out := Redact(text, 500, false)
	if strings.Contains(out, key) || !strings.Contains(out, "[redacted]") {
		t.Fatalf("key leaked: %s", out)
	}
	if !strings.Contains(out, "vpn.example.com:51820") || !strings.Contains(out, "STOPPED") {
		t.Fatalf("useful text dropped: %s", out)
	}
}

func TestNormalizeRejectsUnknownStage(t *testing.T) {
	if _, err := Normalize(Report{UserID: 1, DeviceID: 2, Stage: "panic", ErrorCode: "HANDSHAKE_TIMEOUT"}); err == nil {
		t.Fatal("accepted unknown stage")
	}
	row, err := Normalize(Report{
		UserID: 26, DeviceID: 28, Stage: "handshake", ErrorCode: "HANDSHAKE_TIMEOUT",
		ErrorText: "线路握手超时", ServiceState: "RUNNING", LogSummary: "service=RUNNING",
		AppVersion: "1.1.0+151", Platform: "windows",
	})
	if err != nil || row.Stage != "handshake" || row.AppVersion != "1.1.0+151" || row.Platform != "windows" {
		t.Fatalf("%+v %v", row, err)
	}
}

func TestNormalizeKeepsLogTail(t *testing.T) {
	row, err := Normalize(Report{
		UserID: 1, DeviceID: 2, Stage: "install", ErrorCode: "CONNECT_FAILED",
		LogSummary: strings.Repeat("a", 2500) + "TAIL",
	})
	if err != nil || !strings.HasSuffix(row.LogSummary, "TAIL") || len([]rune(row.LogSummary)) != 2000 {
		got := ""
		if row.LogSummary != "" && len(row.LogSummary) >= 8 {
			got = row.LogSummary[len(row.LogSummary)-8:]
		}
		t.Fatalf("tail %d %q %v", len([]rune(row.LogSummary)), got, err)
	}
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}
