package worldvpn

import (
	"errors"
	"os"
	"strings"
	"time"
)

// Settings 是批发接入的运行时开关。用户名和密码只从环境变量读取，调用方不得把它们写进仓库或日志。
type Settings struct {
	BaseURL     string
	Username    string
	Password    string
	Sandbox     bool
	LiveConfirm bool
	// SyncAfter 非零时，只处理该时刻之后完成的订单、兑换和试用。
	// 零值会在第一次启用时把当前时间写入 worldvpn_state，不回填更早的记录。
	// 不要把它设成很早的时间：那会给历史订单补开批发账号。
	SyncAfter time.Time
}

const defaultBaseURL = "https://www.worldvpn.net/api"

// LoadSettings 读取批发配置。WORLDVPN_SANDBOX 缺省为 true。
// 用户名或密码为空时 Enabled 为 false，现有自建节点不受影响，也不会向外发请求。
// 关闭沙箱还必须同时设置 WORLDVPN_LIVE_CONFIRM=yes，否则仍然视为未启用，避免误扣点数。
func LoadSettings() (Settings, error) {
	var s Settings
	raw := strings.TrimSpace(os.Getenv("WORLDVPN_SANDBOX"))
	s.Sandbox = true
	switch raw {
	case "", "true":
		s.Sandbox = true
	case "false":
		s.Sandbox = false
	default:
		return s, errors.New("WORLDVPN_SANDBOX 只接受 true 或 false")
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("WORLDVPN_API_BASE")), "/")
	if base == "" {
		base = defaultBaseURL
	}
	if err := validateBaseURL(base); err != nil {
		return s, err
	}
	s.BaseURL = base
	s.Username = os.Getenv("WORLDVPN_RESELLER_USERNAME")
	s.Password = os.Getenv("WORLDVPN_RESELLER_PASSWORD")
	s.LiveConfirm = os.Getenv("WORLDVPN_LIVE_CONFIRM") == "yes"
	if v := strings.TrimSpace(os.Getenv("WORLDVPN_SYNC_AFTER")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return s, errors.New("WORLDVPN_SYNC_AFTER 必须是 RFC3339 时间")
		}
		s.SyncAfter = t.UTC()
	}
	if strings.TrimSpace(s.Username) == "" || s.Password == "" {
		return s, nil
	}
	if strings.ContainsAny(s.Username, "\r\n") || strings.ContainsAny(s.Password, "\r\n") {
		return s, errors.New("批发账号环境变量含有换行")
	}
	return s, nil
}

func validateBaseURL(base string) error {
	if strings.HasPrefix(base, "https://") {
		return nil
	}
	if strings.HasPrefix(base, "http://127.0.0.1") || strings.HasPrefix(base, "http://localhost") {
		return nil
	}
	return errors.New("WORLDVPN_API_BASE 必须是 https，或本机测试地址")
}

// Enabled 为 true 时才会访问 WorldVPN。沙箱只要有账号密码即可；真实调用还要显式确认。
func (s Settings) Enabled() bool {
	if strings.TrimSpace(s.Username) == "" || s.Password == "" {
		return false
	}
	if s.Sandbox {
		return true
	}
	return s.LiveConfirm
}
