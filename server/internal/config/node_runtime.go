package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

type NodeRuntime struct {
	Enabled                                  bool
	NodeID, Binary, ManifestFile, RuntimeDir string
}

func LoadNodeRuntime() (NodeRuntime, error) {
	c := NodeRuntime{NodeID: os.Getenv("NODE_WG_NODE_ID"), Binary: os.Getenv("NODE_WG_BINARY"), ManifestFile: os.Getenv("NODE_WG_MANIFEST_FILE"), RuntimeDir: os.Getenv("NODE_WG_RUNTIME_DIR")}
	var e error
	c.Enabled, e = strictBool("NODE_WG_OBSERVER_ENABLED", false)
	if e != nil || !c.Enabled {
		return c, e
	}
	n, e := strconv.ParseInt(c.NodeID, 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != c.NodeID {
		return c, errors.New("需要有效的节点ID")
	}
	for _, path := range []string{c.Binary, c.ManifestFile, c.RuntimeDir} {
		if !filepath.IsAbs(path) {
			return c, errors.New("WireGuard核对程序需要绝对文件路径")
		}
	}
	return c, nil
}
