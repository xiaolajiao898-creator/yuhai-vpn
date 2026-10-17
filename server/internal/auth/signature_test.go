package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/url"
	"testing"
	"time"
)

// 规范化向量：与 apps/mobile/test/request_signer_test.dart 共用，两端必须得到完全相同的字符串。
func TestCanonicalRequestVector(t *testing.T) {
	q := url.Values{"limit": {"20"}, "cursor": {"a b"}, "z": {"2", "1"}}
	got := CanonicalRequest("post", "/v1/orders", q, "1789110000", "h7Qm0x2Zk9rT4pWbJ1eYvA", "0b6f3c1e-8a52-4d7e-9f10-2c4b7d9e1a33", []byte(`{"a":1}`))
	sum := sha256.Sum256([]byte(`{"a":1}`))
	want := "YH1-ECDSA-P256-SHA256\nPOST\n/v1/orders\ncursor=a+b&limit=20&z=1&z=2\n1789110000\nh7Qm0x2Zk9rT4pWbJ1eYvA\n0b6f3c1e-8a52-4d7e-9f10-2c4b7d9e1a33\n" + hexOf(sum[:])
	if got != want {
		t.Fatalf("canonical mismatch:\n%s", got)
	}
	empty := CanonicalRequest("GET", "/v1/me", nil, "1", "n", "d", nil)
	if empty[len(empty)-64:] != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("空请求体应为空串哈希")
	}
}

func TestSignAndVerify(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	pub, err := ParsePublicKey(base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalRequest("POST", "/v1/auth/register", nil, "1", "n", "d", []byte("{}"))
	digest := sha256.Sum256([]byte(canonical))
	sig, _ := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if !Verify(pub, canonical, base64.StdEncoding.EncodeToString(sig)) {
		t.Fatal("verify should pass")
	}
	if Verify(pub, canonical+"x", base64.StdEncoding.EncodeToString(sig)) {
		t.Fatal("tampered canonical should fail")
	}
}

func TestCheckTimestamp(t *testing.T) {
	now := time.Unix(1_789_110_000, 0)
	if CheckTimestamp("1789110100", now) != nil || CheckTimestamp("1789109700", now) != nil {
		t.Fatal("300s 内应通过")
	}
	if CheckTimestamp("1789110301", now) == nil || CheckTimestamp("x", now) == nil {
		t.Fatal("超出 300s 或非法应拒绝")
	}
}

func hexOf(b []byte) string {
	const h = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2], out[i*2+1] = h[x>>4], h[x&15]
	}
	return string(out)
}

func TestCheckTimestampRejectsDurationOverflow(t *testing.T) {
	now := time.Unix(2000000000, 0)
	for _, ts := range []string{"32503680000", "9223372036854775807", "-9223372036854775808"} {
		if err := CheckTimestamp(ts, now); err == nil {
			t.Fatalf("extreme timestamp %s bypassed freshness", ts)
		}
	}
}
