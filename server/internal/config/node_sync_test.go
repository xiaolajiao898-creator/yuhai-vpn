package config

import (
	"os"
	"strings"
	"testing"
)

// 下发开关是这个项目里唯一一个会改写内核状态的开关，所以它的默认值与严格性要单独盯住。
func TestNodeSyncGateAndApplySwitch(t *testing.T) {
	// 什么都不配时同步与下发都必须是关的。
	t.Setenv("NODE_WG_SYNC_ENABLED", "false")
	c, e := LoadNodeSync()
	if e != nil || c.Enabled || c.ApplyEnabled {
		t.Fatal("默认必须同步与下发双关", c, e)
	}

	// 开关只接受严格的 true / false。
	for _, v := range []string{"TRUE", "1", "yes", "on", " true"} {
		t.Setenv("NODE_WG_SYNC_ENABLED", v)
		if _, e := LoadNodeSync(); e == nil {
			t.Fatalf("同步开关接受了宽松取值 %q", v)
		}
	}

	t.Setenv("NODE_WG_SYNC_ENABLED", "true")
	t.Setenv("NODE_CONTROL_URL", "https://control.invalid")
	for k, v := range map[string]string{
		"NODE_AGENT_CERT":        "/etc/yuhai/node.crt",
		"NODE_AGENT_KEY":         "/etc/yuhai/node.key",
		"NODE_CONTROL_CA":        "/etc/yuhai/ca.crt",
		"NODE_WG_SYNC_STATE_DIR": "/run/yuhai-sync",
	} {
		t.Setenv(k, v)
	}

	// 同步开着、下发没配：下发必须仍然是关的。
	c, e = LoadNodeSync()
	if e != nil || !c.Enabled || c.ApplyEnabled {
		t.Fatal("下发开关未显式打开时必须保持关闭", c, e)
	}

	// 下发开关同样只接受严格取值。
	for _, v := range []string{"TRUE", "1", "yes"} {
		t.Setenv("NODE_WG_APPLY_ENABLED", v)
		if _, e := LoadNodeSync(); e == nil {
			t.Fatalf("下发开关接受了宽松取值 %q", v)
		}
	}

	// 打开下发就必须给出 wg 可执行文件的绝对路径，否则拒绝启动。
	t.Setenv("NODE_WG_APPLY_ENABLED", "true")
	for _, v := range []string{"", "wg", "./wg", "bin/wg"} {
		t.Setenv("NODE_WG_BINARY", v)
		if _, e := LoadNodeSync(); e == nil {
			t.Fatalf("下发开启却接受了非绝对路径 %q", v)
		}
	}
	t.Setenv("NODE_WG_BINARY", "/usr/bin/wg")
	c, e = LoadNodeSync()
	if e != nil || !c.ApplyEnabled || c.WGBinary != "/usr/bin/wg" {
		t.Fatal("合法下发配置被拒绝", c, e)
	}

	// 同步关闭时，即便下发开关写成 true 也不该让进程跑起来做任何事。
	t.Setenv("NODE_WG_SYNC_ENABLED", "false")
	c, e = LoadNodeSync()
	if e != nil || c.Enabled {
		t.Fatal("同步关闭时不应报错也不应启用", c, e)
	}
	if c.ApplyEnabled {
		t.Fatal("同步关闭时下发开关不得生效")
	}
}

// 撤销执行回执开关：默认关闭，且只接受严格取值。打开它不会改变任何节点上的实际状态，
// 只是允许 worker 为已收敛的节点登记确认记录。
func TestRemovalReceiptsGateIsStrictAndDefaultsOff(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://fixture.invalid/test")
	t.Setenv("SECRET_PEPPER_HEX", strings.Repeat("01", 32))
	t.Setenv("JWT_ED25519_SEED_HEX", strings.Repeat("02", 32))
	t.Setenv("FIELD_ENC_KEY_HEX", strings.Repeat("03", 32))
	os.Unsetenv("NODE_WG_RECEIPTS_ENABLED")
	c, e := Load()
	if e != nil || c.RemovalReceiptsEnabled {
		t.Fatal("默认必须关闭", c.RemovalReceiptsEnabled, e)
	}
	for _, v := range []string{"TRUE", "1", "yes", "on", ""} {
		t.Setenv("NODE_WG_RECEIPTS_ENABLED", v)
		if _, e := Load(); e == nil {
			t.Fatalf("接受了宽松取值 %q", v)
		}
	}
	t.Setenv("NODE_WG_RECEIPTS_ENABLED", "false")
	if c, e := Load(); e != nil || c.RemovalReceiptsEnabled {
		t.Fatal("显式 false 应当保持关闭", e)
	}
	t.Setenv("NODE_WG_RECEIPTS_ENABLED", "true")
	if c, e := Load(); e != nil || !c.RemovalReceiptsEnabled {
		t.Fatal("显式 true 应当打开", e)
	}
}
