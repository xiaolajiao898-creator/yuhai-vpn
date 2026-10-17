package fieldenc

import (
	"bytes"
	"testing"
)

func TestEnvelope(t *testing.T) {
	c, _ := New("fek-1", bytes.Repeat([]byte{1}, 32))
	aad := AAD("device_credentials", "proxy_secret_enc", 17)
	blob, err := c.Encrypt([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := c.Decrypt(blob, aad)
	if err != nil || string(pt) != "secret" {
		t.Fatalf("%q %v", pt, err)
	}
	if _, err := c.Decrypt(blob, AAD("device_credentials", "proxy_secret_enc", 18)); err == nil {
		t.Fatal("密文搬移到别的行应失败")
	}
	blob[len(blob)-1] ^= 1
	if _, err := c.Decrypt(blob, aad); err == nil {
		t.Fatal("篡改应失败")
	}
}
