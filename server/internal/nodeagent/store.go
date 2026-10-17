// Package nodeagent sends telemetry only. It does not launch or configure tunnels.
package nodeagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

type State struct {
	Version      int                 `json:"version"`
	NodeID       string              `json:"node_id"`
	Endpoint     string              `json:"endpoint"`
	LastSequence string              `json:"last_sequence"`
	Pending      *nodewire.Heartbeat `json:"pending"`
}
type Store struct {
	dir   string
	lock  *os.File
	state State
}

func strictJSON(raw []byte, out any) error { return nodewire.StrictJSON(raw, out) }
func integer(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, errors.New("invalid sequence")
	}
	return n, nil
}

// Initialization is explicit and requires a brand-new directory. Ordinary starts
// never reset a missing or damaged watermark, even when the process is restarted.
func InitStore(dir, nodeID, endpoint string) error {
	if n, err := integer(nodeID); err != nil || n == 0 {
		return errors.New("invalid node ID")
	}
	if !filepath.IsAbs(dir) {
		return errors.New("state path must be absolute")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return errors.New("initialization requires a new state directory")
	}
	s := &Store{dir: dir}
	return s.write(State{Version: 1, NodeID: nodeID, Endpoint: endpoint, LastSequence: "0"})
}
func OpenStore(dir, nodeID, endpoint string) (*Store, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private state directory unavailable")
	}
	f, err := lockState(filepath.Join(dir, "agent.lock"))
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: f}
	raw, err := readPrivate(filepath.Join(dir, "state.json"), 16384)
	if err != nil {
		s.Close()
		return nil, errors.New("state missing or unreadable; refusing sequence reset")
	}
	var st State
	if strictJSON(raw, &st) != nil || st.Version != 1 || st.NodeID != nodeID || st.Endpoint != endpoint {
		s.Close()
		return nil, errors.New("state corrupt or belongs to another node/control plane")
	}
	if _, err = integer(st.LastSequence); err != nil {
		s.Close()
		return nil, err
	}
	if st.Pending != nil && (st.Pending.Sequence != st.LastSequence || nodewire.ValidateHeartbeat(*st.Pending) != nil) {
		s.Close()
		return nil, errors.New("invalid pending heartbeat")
	}
	s.state = st
	return s, nil
}
func (s *Store) Close() {
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
}
func (s *Store) write(st State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if err = tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err = tmp.Write(raw); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return err
	}
	s.state = st
	return nil
}
