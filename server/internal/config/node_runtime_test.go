package config

import "testing"

func TestNodeRuntimeGateAndMinimalConfiguration(t *testing.T) {
	t.Setenv("NODE_WG_OBSERVER_ENABLED", "false")
	t.Setenv("DATABASE_URL", "invalid unrelated setting")
	if c, e := LoadNodeRuntime(); e != nil || c.Enabled {
		t.Fatal(c, e)
	}
	t.Setenv("NODE_WG_OBSERVER_ENABLED", "TRUE")
	if _, e := LoadNodeRuntime(); e == nil {
		t.Fatal("loose gate")
	}
	t.Setenv("NODE_WG_OBSERVER_ENABLED", "true")
	for k, v := range map[string]string{"NODE_WG_NODE_ID": "9007199254740993", "NODE_WG_BINARY": "/usr/bin/wg", "NODE_WG_MANIFEST_FILE": "/var/lib/yuhai/wg.json", "NODE_WG_RUNTIME_DIR": "/run/yuhai-wg"} {
		t.Setenv(k, v)
	}
	c, e := LoadNodeRuntime()
	if e != nil || !c.Enabled || c.NodeID != "9007199254740993" {
		t.Fatal(c, e)
	}
	for _, v := range []string{"0", "01", "-1", "9223372036854775808"} {
		t.Setenv("NODE_WG_NODE_ID", v)
		if _, e := LoadNodeRuntime(); e == nil {
			t.Fatal("invalid identity")
		}
	}
	t.Setenv("NODE_WG_NODE_ID", "7")
	t.Setenv("NODE_WG_BINARY", "wg")
	if _, e := LoadNodeRuntime(); e == nil {
		t.Fatal("PATH binary allowed")
	}
}
