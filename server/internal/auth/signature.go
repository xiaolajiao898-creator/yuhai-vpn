// Package auth 实现设备请求签名（YH1-ECDSA-P256-SHA256，方案 12.2）与令牌相关逻辑。
package auth

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	SignatureAlgorithm = "YH1-ECDSA-P256-SHA256"
	MaxClockSkew       = 300 * time.Second
	NonceTTL           = 10 * time.Minute

	HeaderDeviceID  = "X-Device-Id"
	HeaderTimestamp = "X-Timestamp"
	HeaderNonce     = "X-Nonce"
	HeaderSignature = "X-Signature"
)

// CanonicalRequest 按方案 12.2 生成签名原文（8 行，以 \n 连接）。
// 客户端 Dart 侧必须实现字节级相同的规范化，两端各自有单元测试，并用同一组向量互测。
func CanonicalRequest(method, path string, query url.Values, timestamp, nonce, deviceID string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{
		SignatureAlgorithm,
		strings.ToUpper(method),
		canonicalPath(path),
		canonicalQuery(query),
		timestamp,
		nonce,
		deviceID,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

func canonicalPath(p string) string {
	if p == "" {
		return "/"
	}
	u := url.URL{Path: p}
	return u.EscapedPath()
}

func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// ParsePublicKey 解析 SPKI Base64 的 P-256 公钥。
func ParsePublicKey(spkiB64 string) (*ecdsa.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(spkiB64)
	if err != nil {
		return nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("not a P-256 key")
	}
	return ec, nil
}

// Verify 用设备公钥验证 ASN.1 DER 编码的 ECDSA 签名（Base64）。
func Verify(pub *ecdsa.PublicKey, canonical string, signatureB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(canonical))
	return ecdsa.VerifyASN1(pub, digest[:], sig)
}

// CheckTimestamp 校验 X-Timestamp 与服务器时间偏差（步骤 1）。
func CheckTimestamp(header string, now time.Time) error {
	ts, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return errors.New("bad timestamp")
	}
	// Comparing bounded timestamps avoids time.Duration's MinInt64 negation
	// overflow, which used to accept timestamps hundreds of years ahead.
	if ts < now.Add(-MaxClockSkew).Unix() || ts > now.Add(MaxClockSkew).Unix() {
		return errors.New("timestamp skew")
	}
	return nil
}

// Headers 从请求中提取签名相关头。
func Headers(r *http.Request) (deviceID, timestamp, nonce, signature string) {
	return r.Header.Get(HeaderDeviceID), r.Header.Get(HeaderTimestamp), r.Header.Get(HeaderNonce), r.Header.Get(HeaderSignature)
}

// Accept the deployed Base64url and legacy UUID encodings, but never allocate
// an unbounded Redis key from a signed request or accept an empty replay token.
func validNonce(value string) bool {
	if len(value) == 22 {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
		return err == nil && len(raw) == 16
	}
	if len(value) == 36 {
		id, err := uuid.Parse(value)
		return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == value
	}
	return false
}
