package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type WGCommand struct{ Binary string }
type cappedOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.overflow = true
		return 0, errors.New("output limit")
	}
	return w.buffer.Write(p)
}
func (w *WGCommand) Show(ctx context.Context, iface, field string) ([]byte, error) {
	if !filepath.IsAbs(w.Binary) || !validWGInterface(iface) {
		return nil, wgObservationError()
	}
	switch field {
	case "public-key", "listen-port", "allowed-ips", "latest-handshakes", "transfer":
	default:
		return nil, wgObservationError()
	}
	if e := checkExecutable(w.Binary); e != nil {
		return nil, wgObservationError()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Binary, "show", iface, field)
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "WG_COLOR_MODE=never", "WG_HIDE_KEYS=always"}
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &cappedOutput{limit: 262144}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil || ctx.Err() != nil || stdout.overflow {
		return nil, wgObservationError()
	}
	return stdout.buffer.Bytes(), nil
}

// SetPeers 是唯一会改写内核状态的命令调用。它只下发 peer 的增删改，
// 绝不涉及接口私钥、监听端口、地址或路由；每个参数在拼进 argv 前重新校验。
func (w *WGCommand) SetPeers(ctx context.Context, iface string, changes []WGPeerChange) error {
	if !filepath.IsAbs(w.Binary) || !validWGInterface(iface) || len(changes) == 0 || len(changes) > 1000 {
		return wgApplyError()
	}
	if e := checkExecutable(w.Binary); e != nil {
		return wgApplyError()
	}
	args := make([]string, 0, 2+len(changes)*4)
	args = append(args, "set", iface)
	seen := map[string]bool{}
	for _, c := range changes {
		if !validPublicKey(c.PublicKey) || seen[c.PublicKey] {
			return wgApplyError()
		}
		seen[c.PublicKey] = true
		args = append(args, "peer", c.PublicKey)
		if c.Remove {
			args = append(args, "remove")
			continue
		}
		if len(c.AllowedIPs) < 1 || len(c.AllowedIPs) > 2 {
			return wgApplyError()
		}
		for _, ip := range c.AllowedIPs {
			if !validPeerIP(ip) {
				return wgApplyError()
			}
		}
		args = append(args, "allowed-ips", strings.Join(c.AllowedIPs, ","))
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Binary, args...)
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "WG_COLOR_MODE=never", "WG_HIDE_KEYS=always"}
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		return wgApplyError()
	}
	return nil
}

// ParseWGRows 把 wg show 的字段输出按项目自身的规则切分，供诊断工具 cmd/wgcheck 复用，
// 确保工具校验的就是业务代码实际使用的那套解析逻辑，而不是另写一份。
func ParseWGRows(raw []byte, max int) ([][]string, error) { return wgRows(raw, max) }
