package config

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

type NodeSync struct {
	Enabled bool
	// ApplyEnabled 打开后 node-sync 会把清单里的 peer 真正下发到内核。
	// 默认 false：未经部署方明确开启，同步只落盘、不改内核。
	ApplyEnabled                                          bool
	WGBinary                                              string
	ControlURL, ClientCert, ClientKey, ServerCA, StateDir string
}

func LoadNodeSync() (NodeSync, error) {
	c := NodeSync{ControlURL: os.Getenv("NODE_CONTROL_URL"), ClientCert: os.Getenv("NODE_AGENT_CERT"), ClientKey: os.Getenv("NODE_AGENT_KEY"), ServerCA: os.Getenv("NODE_CONTROL_CA"), StateDir: os.Getenv("NODE_WG_SYNC_STATE_DIR"), WGBinary: os.Getenv("NODE_WG_BINARY")}
	var e error
	c.Enabled, e = strictBool("NODE_WG_SYNC_ENABLED", false)
	if e != nil || !c.Enabled {
		return c, e
	}
	u, err := url.Parse(c.ControlURL)
	if c.ControlURL == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, errors.New("需要控制面HTTPS地址")
	}
	for _, p := range []string{c.ClientCert, c.ClientKey, c.ServerCA, c.StateDir} {
		if !filepath.IsAbs(p) {
			return c, errors.New("同步证书和状态目录需要绝对路径")
		}
	}
	c.ApplyEnabled, e = strictBool("NODE_WG_APPLY_ENABLED", false)
	if e != nil {
		return c, e
	}
	if c.ApplyEnabled && !filepath.IsAbs(c.WGBinary) {
		return c, errors.New("下发 peer 需要 wg 可执行文件的绝对路径")
	}
	return c, nil
}
