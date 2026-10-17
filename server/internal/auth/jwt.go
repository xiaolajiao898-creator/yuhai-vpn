package auth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrTokenMalformed = errors.New("token malformed")
	ErrTokenSignature = errors.New("token signature invalid")
	ErrTokenExpired   = errors.New("token expired")
)

// Signer 签发与校验 Ed25519 JWT（EdDSA）。不引入第三方 JWT 库：载荷固定、算法固定，杜绝 alg 混淆。
type Signer struct {
	keyID string
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
}

func NewSigner(keyID string, seed []byte) *Signer {
	priv := ed25519.NewKeyFromSeed(seed)
	return &Signer{keyID: keyID, priv: priv, pub: priv.Public().(ed25519.PublicKey)}
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

var b64 = base64.RawURLEncoding

func (s *Signer) Sign(c AccessClaims) (string, error) {
	c.KeyID = s.keyID
	h, err := json.Marshal(header{Alg: "EdDSA", Typ: "JWT", Kid: s.keyID})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sig := ed25519.Sign(s.priv, []byte(signing))
	return signing + "." + b64.EncodeToString(sig), nil
}

// Verify 校验签名与格式；allowExpired 用于刷新令牌接口（访问令牌可过期，但签名必须有效）。
func (s *Signer) Verify(token string, now time.Time, allowExpired bool) (AccessClaims, error) {
	var c AccessClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrTokenMalformed
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, ErrTokenMalformed
	}
	var h header
	if json.Unmarshal(hb, &h) != nil || h.Alg != "EdDSA" || h.Kid != s.keyID {
		return c, ErrTokenMalformed
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(s.pub, []byte(parts[0]+"."+parts[1]), sig) {
		return c, ErrTokenSignature
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil || json.Unmarshal(pb, &c) != nil {
		return c, ErrTokenMalformed
	}
	if !allowExpired && now.Unix() >= c.ExpiresAt {
		return c, ErrTokenExpired
	}
	return c, nil
}

// Thumbprint 计算设备公钥（SPKI Base64）的指纹，写入令牌 cnf.jkt，与数据库公钥比对。
func Thumbprint(spkiB64 string) string {
	sum := sha256.Sum256([]byte(spkiB64))
	return b64.EncodeToString(sum[:])
}
