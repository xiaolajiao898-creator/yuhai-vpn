package nodeagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type runtimeIdentity struct {
	Schema int    `json:"schema"`
	NodeID string `json:"node_id"`
}
type RuntimeStore struct {
	dir, nodeID string
	lock        *os.File
	mu          sync.Mutex
}

func InitRuntimeStore(dir, nodeID string) error {
	n, e := integer(nodeID)
	if e != nil || n == 0 || !filepath.IsAbs(dir) {
		return errors.New("invalid runtime identity")
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return errors.New("runtime initialization requires a new directory")
	}
	raw, _ := json.Marshal(runtimeIdentity{1, nodeID})
	return atomicPrivate(dir, "identity.json", raw)
}
func OpenRuntimeStore(dir, nodeID string) (*RuntimeStore, error) {
	if e := checkRuntimeDir(dir); e != nil {
		return nil, e
	}
	lock, e := lockState(filepath.Join(dir, "runtime.lock"))
	if e != nil {
		return nil, e
	}
	out := &RuntimeStore{dir: dir, nodeID: nodeID, lock: lock}
	raw, e := readPrivate(filepath.Join(dir, "identity.json"), 1024)
	var identity runtimeIdentity
	if e != nil || strictJSON(raw, &identity) != nil || identity.Schema != 1 || identity.NodeID != nodeID {
		out.Close()
		return nil, errors.New("runtime directory identity mismatch")
	}
	if n, e := integer(nodeID); e != nil || n == 0 {
		out.Close()
		return nil, errors.New("invalid runtime identity")
	}
	if e = out.Clear(); e != nil {
		out.Close()
		return nil, e
	}
	return out, nil
}
func (s *RuntimeStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
}
func (s *RuntimeStore) Publish(status RuntimeStatus, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("runtime lock not held")
	}
	raw, e := json.Marshal(status)
	if e != nil {
		return e
	}
	if _, e = parseRuntime(raw, s.nodeID, now); e != nil {
		return errors.New("invalid runtime result")
	}
	return atomicPrivate(s.dir, "runtime.json", raw)
}
func (s *RuntimeStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("runtime lock not held")
	}
	err := os.Remove(filepath.Join(s.dir, "runtime.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot invalidate runtime status")
	}
	return syncDir(s.dir)
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicPrivate(dir, name string, raw []byte) error {
	f, e := os.CreateTemp(dir, ".runtime-*")
	if e != nil {
		return e
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(raw); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(temp, filepath.Join(dir, name)); e != nil {
		return e
	}
	return syncDir(dir)
}
