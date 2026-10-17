// Package fieldenc 实现字段信封加密（方案 12.6）：AES-256-GCM，AAD = 表.列.主键，
// 防止密文在行之间被搬移。生产环境数据密钥由 KMS 解封，开发环境直接来自配置。
package fieldenc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

type Cipher struct {
	keyID string
	aead  cipher.AEAD
}

func New(keyID string, key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{keyID: keyID, aead: aead}, nil
}

// AAD 生成关联数据：表.列.主键。
func AAD(table, column string, pk any) []byte {
	return []byte(fmt.Sprintf("%s.%s.%v", table, column, pk))
}

// Encrypt 输出格式：keyID 长度(1 字节) || keyID || nonce(12) || 密文+tag。
func (c *Cipher) Encrypt(plaintext, aad []byte) ([]byte, error) {
	if len(c.keyID) > 255 {
		return nil, errors.New("key id too long")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(c.keyID)+len(nonce)+len(plaintext)+c.aead.Overhead())
	out = append(out, byte(len(c.keyID)))
	out = append(out, c.keyID...)
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, plaintext, aad), nil
}

func (c *Cipher) Decrypt(blob, aad []byte) ([]byte, error) {
	if len(blob) < 1 {
		return nil, errors.New("blob too short")
	}
	n := int(blob[0])
	if len(blob) < 1+n+c.aead.NonceSize() {
		return nil, errors.New("blob too short")
	}
	if string(blob[1:1+n]) != c.keyID {
		return nil, fmt.Errorf("key id mismatch: %q（轮换期间需保留旧密钥解密）", blob[1:1+n])
	}
	nonce := blob[1+n : 1+n+c.aead.NonceSize()]
	return c.aead.Open(nil, nonce, blob[1+n+c.aead.NonceSize():], aad)
}
