// Package tron 提供波场（TRON）地址校验与链上 TRX 转账查询。
package tron

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
)

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ErrInvalidAddress 表示不是合法的 TRON 主网地址（T 开头的 Base58Check，34 位）。
var ErrInvalidAddress = errors.New("不是有效的 TRON 地址")

// DecodeAddress 校验 Base58Check 并返回 21 字节地址（首字节 0x41）。
func DecodeAddress(s string) ([]byte, error) {
	if len(s) != 34 || s[0] != 'T' {
		return nil, ErrInvalidAddress
	}
	n := new(big.Int)
	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(alphabet, s[i])
		if idx < 0 {
			return nil, ErrInvalidAddress
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(idx)))
	}
	raw := n.Bytes()
	if len(raw) != 25 {
		return nil, ErrInvalidAddress
	}
	payload, sum := raw[:21], raw[21:]
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	if !bytes.Equal(h2[:4], sum) || payload[0] != 0x41 {
		return nil, ErrInvalidAddress
	}
	return payload, nil
}

// EncodeAddress 把 21 字节地址编码为 Base58Check。
func EncodeAddress(payload []byte) string {
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	full := append(append([]byte{}, payload...), h2[:4]...)
	n := new(big.Int).SetBytes(full)
	var out []byte
	mod := new(big.Int)
	base := big.NewInt(58)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, b := range full {
		if b != 0 {
			break
		}
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// ValidAddress 报告 s 是否为合法 TRON 地址。
func ValidAddress(s string) bool {
	_, err := DecodeAddress(s)
	return err == nil
}

// HexAddress 返回小写十六进制形式（41 开头），与 TronGrid 原始交易里的格式一致。
func HexAddress(s string) (string, error) {
	p, err := DecodeAddress(s)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(p), nil
}
