package config

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

type NodeAgent struct {
	Enabled                                                                       bool
	ControlURL, ClientCert, ClientKey, ServerCA, StateDir, RuntimeFile, Interface string
}

func LoadNodeAgent() (NodeAgent, error) {
	c := NodeAgent{ControlURL: os.Getenv("NODE_CONTROL_URL"), ClientCert: os.Getenv("NODE_AGENT_CERT"), ClientKey: os.Getenv("NODE_AGENT_KEY"), ServerCA: os.Getenv("NODE_CONTROL_CA"), StateDir: os.Getenv("NODE_AGENT_STATE_DIR"), RuntimeFile: os.Getenv("NODE_RUNTIME_STATUS_FILE"), Interface: os.Getenv("NODE_NET_INTERFACE")}
	var err error
	c.Enabled, err = strictBool("NODE_AGENT_ENABLED", false)
	if err != nil || !c.Enabled {
		return c, err
	}
	if c.ControlURL == "" || !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`).MatchString(c.Interface) || c.Interface == "lo" {
		return c, errors.New("需要控制面URL与明确的非回环网卡")
	}
	for _, p := range []string{c.ClientCert, c.ClientKey, c.ServerCA, c.StateDir, c.RuntimeFile} {
		if !filepath.IsAbs(p) {
			return c, errors.New("节点证书和状态文件必须使用绝对路径")
		}
	}
	return c, nil
}
