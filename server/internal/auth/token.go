package auth

import "time"

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 30 * 24 * time.Hour
)

// AccessClaims 是访问令牌载荷（Ed25519 签名的 JWT；签发/解析在阶段一第 1–2 周实现）。
type AccessClaims struct {
	Subject       int64  `json:"sub"`     // user_id
	DeviceID      int64  `json:"did"`     // devices.id
	SecretVersion int    `json:"sv"`      // users.secret_version，密钥重置后旧令牌失效（40108）
	KeyThumbprint string `json:"cnf_jkt"` // 设备公钥指纹，与数据库公钥比对
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
	KeyID         string `json:"kid,omitempty"`
}
