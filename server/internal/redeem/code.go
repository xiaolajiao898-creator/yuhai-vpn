// Package redeem 生成和兑换一次性卡密。
// 明文只在生成或导出时出现；库里保存的是去掉连字符后的 SHA-256。
package redeem

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/yuhai-vpn/server/internal/fieldenc"
)

// 去掉 0/O/1/I，避免手抄看错。32 个字符正好整除 256，取模没有偏差。
const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const codeLength = 16

// GenerateCode 返回形如 ABCD-EFGH-JKLM-NPQR 的码。
func GenerateCode() (string, error) {
	var raw [codeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Grow(codeLength + 3)
	for i, x := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(x)%len(alphabet)])
	}
	return b.String(), nil
}

// Normalize 接受大小写、空格和连字符，得到 16 位大写码。格式不对返回错误。
func Normalize(in string) (string, error) {
	if len(in) == 0 || len(in) > 64 {
		return "", errBadCode
	}
	var b strings.Builder
	b.Grow(codeLength)
	for _, r := range in {
		switch r {
		case '-', ' ', '\t':
			continue
		}
		if r > 127 {
			return "", errBadCode
		}
		r = rune(strings.ToUpper(string(r))[0])
		if !strings.ContainsRune(alphabet, r) {
			return "", errBadCode
		}
		b.WriteRune(r)
		if b.Len() > codeLength {
			return "", errBadCode
		}
	}
	if b.Len() != codeLength {
		return "", errBadCode
	}
	return b.String(), nil
}

// Format 把 16 位码分成四组。
func Format(normalized string) string {
	return normalized[0:4] + "-" + normalized[4:8] + "-" + normalized[8:12] + "-" + normalized[12:16]
}

// Hint 是列表里能看的后四位，不能拿来兑换。
func Hint(normalized string) string {
	return normalized[12:16]
}

// Hash 是入库的值。输入必须是 Normalize 的结果。
func Hash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

var errBadCode = errors.New("bad redeem code")

// SealCodes 把一批明文封进字段加密，关联到批次主键，避免密文被挪到别的批次还能解开。
func SealCodes(c *fieldenc.Cipher, batchID int64, codes []string) ([]byte, error) {
	if c == nil || batchID <= 0 || len(codes) == 0 || len(codes) > 500 {
		return nil, errors.New("cannot seal redeem codes")
	}
	plain, err := json.Marshal(codes)
	if err != nil {
		return nil, err
	}
	return c.Encrypt(plain, fieldenc.AAD("redeem_batches", "codes_enc", batchID))
}

// OpenCodes 解出生成时的明文列表。
func OpenCodes(c *fieldenc.Cipher, batchID int64, blob []byte) ([]string, error) {
	if c == nil || batchID <= 0 || len(blob) == 0 {
		return nil, errors.New("cannot open redeem codes")
	}
	plain, err := c.Decrypt(blob, fieldenc.AAD("redeem_batches", "codes_enc", batchID))
	if err != nil {
		return nil, err
	}
	var codes []string
	if err := json.Unmarshal(plain, &codes); err != nil || len(codes) == 0 || len(codes) > 500 {
		return nil, errors.New("sealed redeem codes are invalid")
	}
	seen := make(map[string]struct{}, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		norm, err := Normalize(code)
		if err != nil {
			return nil, errors.New("sealed redeem codes are invalid")
		}
		formatted := Format(norm)
		if _, ok := seen[formatted]; ok {
			return nil, errors.New("sealed redeem codes are invalid")
		}
		seen[formatted] = struct{}{}
		out = append(out, formatted)
	}
	return out, nil
}
