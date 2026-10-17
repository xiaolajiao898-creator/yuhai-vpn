// Package account 实现匿名账户：注册、恢复、设备管理、令牌轮换、首页聚合（方案第 4 章、11.4）。
package account

import "time"

type DeviceInfo struct {
	DeviceUID       string `json:"device_uid"`
	DevicePubKey    string `json:"device_pubkey"`
	Platform        string `json:"platform"`
	Model           string `json:"model"`
	OSVersion       string `json:"os_version"`
	AppVersion      string `json:"app_version"`
	FingerprintHash string `json:"fingerprint_hash"`
}

type InviteHint struct {
	Code   string `json:"code"`
	Method string `json:"method"` // APK_CHANNEL / CLIPBOARD / MANUAL
}

type RegisterInput struct {
	DeviceInfo
	Invite *InviteHint `json:"invite,omitempty"`
}

type RestoreInput struct {
	DeviceInfo
	AccountSecret string `json:"account_secret"`
}

type ReplaceDeviceInput struct {
	DeviceInfo
	RestoreTicket  string `json:"restore_ticket"`
	RemoveDeviceID string `json:"remove_device_id"`
}

type Tokens struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

type RegisterResult struct {
	AccountNo     string    `json:"account_no"`
	AccountSecret string    `json:"account_secret"`
	InviteCode    string    `json:"invite_code"`
	InviteBound   bool      `json:"invite_bound"`
	DeviceID      string    `json:"device_id"`
	Tokens        Tokens    `json:"tokens"`
	ServerTime    time.Time `json:"server_time"`
	userID        int64
}

type RestoreResult struct {
	AccountNo  string    `json:"account_no"`
	DeviceID   string    `json:"device_id"`
	Tokens     Tokens    `json:"tokens"`
	ServerTime time.Time `json:"server_time"`
}

type DeviceSummary struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Platform    string     `json:"platform"`
	Model       string     `json:"model,omitempty"`
	IsCurrent   bool       `json:"is_current"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type TrafficPack struct {
	ID             string    `json:"id"`
	RemainingBytes int64     `json:"remaining_bytes"`
	ExpireAt       time.Time `json:"expire_at"`
}

type Entitlement struct {
	CanConnect       bool          `json:"can_connect"`
	BlockedReason    *string       `json:"blocked_reason"`
	Tier             int           `json:"tier"`
	PlanName         string        `json:"plan_name,omitempty"`
	ExpireAt         *time.Time    `json:"expire_at"`
	PeriodQuotaBytes *int64        `json:"period_quota_bytes"`
	PeriodUsedBytes  int64         `json:"period_used_bytes"`
	PeriodResetAt    *time.Time    `json:"period_reset_at"`
	TrafficPacks     []TrafficPack `json:"traffic_packs"`
	DeviceLimit      int           `json:"device_limit"`
	DeviceCount      int           `json:"device_count"`
}

type PendingOrder struct {
	OrderNo   string    `json:"order_no"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SecurityNotice struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	CreatedAt  time.Time `json:"created_at"`
	DeviceName string    `json:"device_name,omitempty"`
}

type Me struct {
	AccountNo           string           `json:"account_no"`
	LoginAddress        string           `json:"login_address,omitempty"`
	Status              string           `json:"status"`
	BackupAcknowledged  bool             `json:"backup_acknowledged"`
	Entitlement         Entitlement      `json:"entitlement"`
	Balance             string           `json:"balance"`
	PendingOrder        *PendingOrder    `json:"pending_order"`
	SecurityNotices     []SecurityNotice `json:"security_notices"`
	AnnouncementsUnread int              `json:"announcements_unread"`
	ServerTime          time.Time        `json:"server_time"`
}
