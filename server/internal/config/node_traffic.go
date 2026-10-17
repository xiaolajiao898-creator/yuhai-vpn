package config

import (
	"errors"
	"os"
	"path/filepath"
)

// 节点侧流量采集与上报。默认关闭；关闭时进程启动后立即退出，不读取任何其他配置。
type NodeTraffic struct {
	Enabled                                     bool
	ControlURL, ClientCert, ClientKey, ServerCA string
	StateDir, ManifestFile, WGBinary, Interface string
}

func LoadNodeTraffic() (NodeTraffic, error) {
	c := NodeTraffic{
		ControlURL:   os.Getenv("NODE_CONTROL_URL"),
		ClientCert:   os.Getenv("NODE_AGENT_CERT"),
		ClientKey:    os.Getenv("NODE_AGENT_KEY"),
		ServerCA:     os.Getenv("NODE_CONTROL_CA"),
		StateDir:     os.Getenv("NODE_TRAFFIC_STATE_DIR"),
		ManifestFile: os.Getenv("NODE_WG_MANIFEST_FILE"),
		WGBinary:     os.Getenv("NODE_WG_BINARY"),
		Interface:    os.Getenv("NODE_WG_INTERFACE"),
	}
	var err error
	if c.Enabled, err = strictBool("NODE_TRAFFIC_REPORT_ENABLED", false); err != nil || !c.Enabled {
		return c, err
	}
	if c.ControlURL == "" {
		return c, errors.New("需要控制面HTTPS地址")
	}
	for _, p := range []string{c.ClientCert, c.ClientKey, c.ServerCA, c.StateDir, c.ManifestFile, c.WGBinary} {
		if !filepath.IsAbs(p) {
			return c, errors.New("上报证书、状态目录、租约文件与 wg 可执行文件都需要绝对路径")
		}
	}
	return c, nil
}
