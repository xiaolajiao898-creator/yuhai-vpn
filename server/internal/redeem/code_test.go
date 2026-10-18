package redeem

import (
	"strings"
	"testing"

	"github.com/yuhai-vpn/server/internal/fieldenc"
)

func TestGenerateCodeUniqueAndTypeable(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 2000; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := seen[code]; ok {
			t.Fatalf("duplicate code %s", code)
		}
		seen[code] = struct{}{}
		norm, err := Normalize(code)
		if err != nil {
			t.Fatal(code, err)
		}
		if Format(norm) != code {
			t.Fatalf("format roundtrip %s", code)
		}
		parts := strings.Split(code, "-")
		if len(parts) != 4 {
			t.Fatalf("groups %s", code)
		}
		for _, p := range parts {
			if len(p) != 4 {
				t.Fatalf("group length %s", code)
			}
			for _, r := range p {
				if !strings.ContainsRune(alphabet, r) {
					t.Fatalf("ambiguous or lowercase char %q in %s", r, code)
				}
			}
		}
		if strings.ContainsAny(code, "01OI") {
			t.Fatalf("ambiguous chars in %s", code)
		}
	}
	if len(seen) != 2000 {
		t.Fatalf("unique count %d", len(seen))
	}
}

func TestNormalizeAcceptsHumanInput(t *testing.T) {
	norm, err := Normalize("  abcd-efgh-jklm-npqr ")
	if err != nil || norm != "ABCDEFGHJKLMNPQR" {
		t.Fatal(norm, err)
	}
	if Hash(norm) == norm || len(Hash(norm)) != 64 {
		t.Fatal("hash should not be the plaintext")
	}
	if Hash(norm) != Hash(norm) {
		t.Fatal("hash unstable")
	}
	for _, bad := range []string{"", "ABCD-EFGH-JKLM-NPQ0", "ABCD-EFGH-JKLM-NPQI", "ABCD-EFGH-JKLM-NPQO", "ABCD-EFGH-JKLM-NPQ1", "SHORT", strings.Repeat("A", 17)} {
		if _, err := Normalize(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestSealCodesRoundTrip(t *testing.T) {
	c, err := fieldenc.New("redeem-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	codes := []string{"ABCD-EFGH-JKLM-NPQR", "2345-6789-ABCD-EFGH"}
	blob, err := SealCodes(c, 7, codes)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		if strings.Contains(string(blob), code) || strings.Contains(string(blob), strings.ReplaceAll(code, "-", "")) {
			t.Fatal("ciphertext contains plaintext")
		}
	}
	out, err := OpenCodes(c, 7, blob)
	if err != nil || len(out) != 2 || out[0] != codes[0] || out[1] != codes[1] {
		t.Fatal(out, err)
	}
	if _, err := OpenCodes(c, 8, blob); err == nil {
		t.Fatal("aad must bind the batch id")
	}
}
