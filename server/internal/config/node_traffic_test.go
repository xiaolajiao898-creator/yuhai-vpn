package config

import "testing"

// 节点侧上报开关：默认关闭，只接受严格取值；打开后所有路径必须是绝对路径。
func TestNodeTrafficGateAndPaths(t *testing.T) {
	t.Setenv("NODE_TRAFFIC_REPORT_ENABLED", "false")
	c, e := LoadNodeTraffic()
	if e != nil || c.Enabled {
		t.Fatal("默认必须关闭", c, e)
	}
	for _, v := range []string{"TRUE", "1", "yes", " true"} {
		t.Setenv("NODE_TRAFFIC_REPORT_ENABLED", v)
		if _, e := LoadNodeTraffic(); e == nil {
			t.Fatalf("接受了宽松取值 %q", v)
		}
	}
	t.Setenv("NODE_TRAFFIC_REPORT_ENABLED", "true")
	if _, e := LoadNodeTraffic(); e == nil {
		t.Fatal("缺少控制面地址却启动")
	}
	t.Setenv("NODE_CONTROL_URL", "https://control.invalid")
	for k, v := range map[string]string{
		"NODE_AGENT_CERT":        "/etc/yuhai/node.crt",
		"NODE_AGENT_KEY":         "/etc/yuhai/node.key",
		"NODE_CONTROL_CA":        "/etc/yuhai/ca.crt",
		"NODE_TRAFFIC_STATE_DIR": "/run/yuhai-traffic",
		"NODE_WG_MANIFEST_FILE":  "/run/yuhai-sync/manifest.json",
		"NODE_WG_BINARY":         "/usr/bin/wg",
	} {
		t.Setenv(k, v)
	}
	c, e = LoadNodeTraffic()
	if e != nil || !c.Enabled || c.WGBinary != "/usr/bin/wg" {
		t.Fatal("合法配置被拒绝", c, e)
	}
	// 任何一处相对路径都必须拒绝：状态目录或租约文件写错会导致计费数据错位。
	for _, k := range []string{"NODE_AGENT_CERT", "NODE_AGENT_KEY", "NODE_CONTROL_CA", "NODE_TRAFFIC_STATE_DIR", "NODE_WG_MANIFEST_FILE", "NODE_WG_BINARY"} {
		original := c
		t.Setenv(k, "relative/path")
		if _, e := LoadNodeTraffic(); e == nil {
			t.Fatalf("%s 接受了相对路径", k)
		}
		switch k {
		case "NODE_AGENT_CERT":
			t.Setenv(k, original.ClientCert)
		case "NODE_AGENT_KEY":
			t.Setenv(k, original.ClientKey)
		case "NODE_CONTROL_CA":
			t.Setenv(k, original.ServerCA)
		case "NODE_TRAFFIC_STATE_DIR":
			t.Setenv(k, original.StateDir)
		case "NODE_WG_MANIFEST_FILE":
			t.Setenv(k, original.ManifestFile)
		case "NODE_WG_BINARY":
			t.Setenv(k, original.WGBinary)
		}
	}
}
