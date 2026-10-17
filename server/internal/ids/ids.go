// Package ids 生成账户号、账户密钥、邀请码、订单号等业务标识（方案 4.1、6.1、7.1）。
package ids

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Crockford Base32 去除 I L O U：账户密钥字符集
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// 邀请码字符集：去掉 0 1 I L O
const inviteAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

const (
	AccountNoLen     = 10
	SecretBodyLen    = 20 // 20 × 5 bit = 100 bit 熵
	InviteCodeLen    = 6
	OrderNoRandomLen = 8
)

var secretRe = regexp.MustCompile(`^YH-?([0-9A-HJKMNP-TV-Z]{4})-?([0-9A-HJKMNP-TV-Z]{4})-?([0-9A-HJKMNP-TV-Z]{4})-?([0-9A-HJKMNP-TV-Z]{4})-?([0-9A-HJKMNP-TV-Z]{4})$`)

// NewAccountNo 生成 10 位数字账户号（首位非 0）。唯一性由数据库约束保证，冲突时调用方重试。
func NewAccountNo() (string, error) {
	var b strings.Builder
	first, err := randInt(9)
	if err != nil {
		return "", err
	}
	b.WriteByte(byte('1' + first))
	for i := 1; i < AccountNoLen; i++ {
		n, err := randInt(10)
		if err != nil {
			return "", err
		}
		b.WriteByte(byte('0' + n))
	}
	return b.String(), nil
}

// NewAccountSecret 生成账户密钥，返回带分组的展示形式 YH-XXXX-XXXX-XXXX-XXXX-XXXX。
func NewAccountSecret() (string, error) {
	body, err := randString(crockford, SecretBodyLen)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, 5)
	for i := 0; i < SecretBodyLen; i += 4 {
		parts = append(parts, body[i:i+4])
	}
	return "YH-" + strings.Join(parts, "-"), nil
}

// NormalizeSecret 接受用户输入（大小写、有无连字符、常见混淆字符）并返回规范形式；无效返回 error。
// 调用方对无效密钥统一返回 40104，不区分格式错误与不存在。
func NormalizeSecret(input string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(input))
	s = strings.NewReplacer("I", "1", "L", "1", "O", "0", "U", "V", " ", "").Replace(s)
	m := secretRe.FindStringSubmatch(s)
	if m == nil {
		return "", errors.New("invalid secret")
	}
	return "YH-" + strings.Join(m[1:], "-"), nil
}

// SecretLookup 计算 HMAC-SHA256(pepper, 规范化密钥) 的十六进制，即 users.secret_lookup。
func SecretLookup(pepper []byte, normalizedSecret string) string {
	h := hmac.New(sha256.New, pepper)
	h.Write([]byte(normalizedSecret))
	return hex.EncodeToString(h.Sum(nil))
}

func NewInviteCode() (string, error) { return randString(inviteAlphabet, InviteCodeLen) }

// NewOrderNo 生成订单号：YH + yymmdd + 8 位随机，例如 YH2609118K3Q7TZ2。
func NewOrderNo(now time.Time) (string, error) {
	r, err := randString(inviteAlphabet, OrderNoRandomLen)
	if err != nil {
		return "", err
	}
	return "YH" + now.UTC().Format("060102") + r, nil
}

func randString(alphabet string, n int) (string, error) {
	b := make([]byte, n)
	for i := range b {
		k, err := randInt(len(alphabet))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[k]
	}
	return string(b), nil
}

func randInt(max int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()), nil
}
