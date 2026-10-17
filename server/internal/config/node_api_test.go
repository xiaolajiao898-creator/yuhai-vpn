package config

import (
	"os"
	"testing"
)

func nodeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	t.Setenv("NODE_API_ENABLED", "true")
	t.Setenv("NODE_API_LISTEN", "127.0.0.1:18443")
	t.Setenv("NODE_DATABASE_URL", "postgres://fixture.invalid/node")
	t.Setenv("NODE_TLS_SERVER_CERT", "/fixture/server.crt")
	t.Setenv("NODE_TLS_SERVER_KEY", "/fixture/server.key")
	t.Setenv("NODE_TLS_CLIENT_CA", "/fixture/node-ca.crt")
}

func TestNodeAPIConfigurationFailClosed(t *testing.T) {
	nodeEnv(t)
	if c, err := LoadNodeAPI(); err != nil || !c.Enabled {
		t.Fatal("valid isolated settings rejected", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"NODE_API_ENABLED", ""}, {"NODE_API_ENABLED", "TRUE"}, {"NODE_API_ENABLED", "1"},
		{"APP_ENV", "prod"}, {"NODE_DATABASE_URL", ""}, {"NODE_TLS_SERVER_CERT", ""}, {"NODE_TLS_SERVER_KEY", "relative.key"}, {"NODE_TLS_CLIENT_CA", ""},
		{"NODE_API_LISTEN", ":8443"}, {"NODE_API_LISTEN", "localhost:8443"}, {"NODE_API_LISTEN", "127.0.0.1:0"}, {"NODE_API_LISTEN", "127.0.0.1:65536"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := LoadNodeAPI(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, value := range []string{"false", "unset"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NODE_API_ENABLED", "false")
			if value == "unset" {
				if err := os.Unsetenv("NODE_API_ENABLED"); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("NODE_DATABASE_URL", "")
			t.Setenv("NODE_TLS_SERVER_CERT", "")
			t.Setenv("NODE_TLS_SERVER_KEY", "")
			t.Setenv("NODE_TLS_CLIENT_CA", "")
			if c, err := LoadNodeAPI(); err != nil || c.Enabled {
				t.Fatal("default must not start a listener", err)
			}
		})
	}
}

// 流量上报开关：默认关闭，只接受严格取值。打开后节点上报才会进入计费路径。
func TestNodeAPITrafficGateDefaultsOff(t *testing.T) {
	t.Setenv("NODE_API_ENABLED", "false")
	c, e := LoadNodeAPI()
	if e != nil || c.TrafficReportEnabled {
		t.Fatal("默认必须关闭", c.TrafficReportEnabled, e)
	}
	for _, v := range []string{"TRUE", "1", "yes", ""} {
		t.Setenv("NODE_TRAFFIC_API_ENABLED", v)
		if _, e := LoadNodeAPI(); e == nil {
			t.Fatalf("接受了宽松取值 %q", v)
		}
	}
	t.Setenv("NODE_TRAFFIC_API_ENABLED", "true")
	if c, e := LoadNodeAPI(); e != nil || !c.TrafficReportEnabled {
		t.Fatal("显式 true 应当打开", e)
	}
}

func TestNodeAPIEnrollDefaultsOffAndRequiresAbsolutePaths(t *testing.T) {
	nodeEnv(t)
	t.Setenv("NODE_ENROLL_ENABLED", "false")
	if err := os.Unsetenv("NODE_ENROLL_ENABLED"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadNodeAPI()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnrollEnabled || cfg.EnrollCACert != "" || cfg.EnrollCAKey != "" {
		t.Fatalf("节点注册必须默认关闭：%+v", cfg)
	}

	// 只接受严格的 true/false。
	for _, bad := range []string{"1", "TRUE", "yes", ""} {
		t.Setenv("NODE_ENROLL_ENABLED", bad)
		if _, err := LoadNodeAPI(); err == nil && bad != "" {
			t.Fatalf("NODE_ENROLL_ENABLED=%q 应被拒绝", bad)
		}
	}

	// 打开但没给 CA 路径：必须拒绝启动，而不是静默地跑一个发不出证书的注册端点。
	t.Setenv("NODE_ENROLL_ENABLED", "true")
	if _, err := LoadNodeAPI(); err == nil {
		t.Fatal("启用注册却没配 CA 路径，应当拒绝启动")
	}
	t.Setenv("NODE_ENROLL_CA_CERT", "relative/ca.crt")
	t.Setenv("NODE_ENROLL_CA_KEY", "/abs/ca.key")
	if _, err := LoadNodeAPI(); err == nil {
		t.Fatal("相对路径应当被拒绝")
	}
	t.Setenv("NODE_ENROLL_CA_CERT", "/abs/ca.crt")
	cfg, err = LoadNodeAPI()
	if err != nil || !cfg.EnrollEnabled || cfg.EnrollCACert != "/abs/ca.crt" || cfg.EnrollCAKey != "/abs/ca.key" {
		t.Fatalf("显式配置未生效：%+v %v", cfg, err)
	}

	// 关闭时即便配了路径也要清空，避免「以为开着其实没开」。
	t.Setenv("NODE_ENROLL_ENABLED", "false")
	cfg, err = LoadNodeAPI()
	if err != nil || cfg.EnrollEnabled || cfg.EnrollCACert != "" || cfg.EnrollCAKey != "" {
		t.Fatalf("关闭时残留了 CA 路径：%+v %v", cfg, err)
	}
}
