// Package trial 给没有权益记录的账户发放 24 小时试用。
package trial

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/yuhai-vpn/server/internal/httpx"
)

const (
	DurationHours = 24
	Tier          = 1
	DeviceLimit   = 3
	configKey     = "trial.enabled"
)

var (
	ErrDisabled   = httpx.NewError(httpx.ErrTrialDisabled, "免费试用暂未开放")
	ErrNotNew     = httpx.NewError(httpx.ErrConflict, "这个账户已经开通过套餐，不能再领试用").WithDetails(map[string]any{"reason": "NOT_NEW"})
	ErrDeviceUsed = httpx.NewError(httpx.ErrConflict, "这台设备已经领取过免费试用").WithDetails(map[string]any{"reason": "DEVICE_USED"})
	ErrBadInstall = httpx.NewError(httpx.ErrBadRequest, "设备标识不正确")
)

// ParseInstallID 接受小写或大写的标准 UUID。空值和带花括号的写法都拒绝。
func ParseInstallID(raw string) (uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "{}") {
		return uuid.UUID{}, errors.New("empty")
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.UUID{}, errors.New("bad")
	}
	return id, nil
}

// Enabled 解释试用开关。
// 环境变量 YUHAI_TRIAL_ENABLED 为 0 / false / off 时强制关闭，优先于数据库。
// 数据库缺这一行时视为开启（迁移会写入 true）。值不是 JSON 布尔时视为关闭，避免脏数据继续发试用。
func Enabled(raw []byte, missing bool) bool {
	if envDisablesTrial() {
		return false
	}
	if missing {
		return true
	}
	var on bool
	if json.Unmarshal(raw, &on) != nil {
		return false
	}
	return on
}

func envDisablesTrial() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("YUHAI_TRIAL_ENABLED"))) {
	case "0", "false", "off", "no":
		return true
	default:
		return false
	}
}
