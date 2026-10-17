package nodeagent

import (
	"encoding/json"
	"errors"
	"github.com/yuhai-vpn/server/internal/nodewire"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type WGSyncIdentity struct {
	Schema   int    `json:"schema"`
	NodeID   string `json:"node_id"`
	Endpoint string `json:"endpoint"`
}
type WGSyncStore struct {
	mu       sync.Mutex
	dir      string
	identity WGSyncIdentity
	lock     *os.File
}

func InitWGSyncStore(dir, nodeID, origin string) error {
	endpoint, e := Endpoint(origin)
	if e != nil {
		return e
	}
	n, e := integer(nodeID)
	if e != nil || n == 0 || !filepath.IsAbs(dir) {
		return errors.New("invalid sync identity")
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return errors.New("sync initialization requires new directory")
	}
	s := &WGSyncStore{dir: dir, identity: WGSyncIdentity{1, nodeID, endpoint}}
	raw, _ := json.Marshal(s.identity)
	return s.atomic("identity.json", raw)
}
func OpenWGSyncStore(dir, nodeID, origin string) (*WGSyncStore, error) {
	endpoint, e := Endpoint(origin)
	if e != nil {
		return nil, e
	}
	if e = checkRuntimeDir(dir); e != nil {
		return nil, e
	}
	f, e := lockState(filepath.Join(dir, "sync.lock"))
	if e != nil {
		return nil, e
	}
	s := &WGSyncStore{dir: dir, lock: f, identity: WGSyncIdentity{1, nodeID, endpoint}}
	raw, e := readPrivate(filepath.Join(dir, "identity.json"), 4096)
	var identity WGSyncIdentity
	if e != nil || strictJSON(raw, &identity) != nil || identity != s.identity {
		s.Close()
		return nil, errors.New("sync identity unavailable or mismatched")
	}
	if e = s.Clear(); e != nil {
		s.Close()
		return nil, e
	}
	return s, nil
}
func (s *WGSyncStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
}
func (s *WGSyncStore) atomic(name string, raw []byte) error {
	f, e := os.CreateTemp(s.dir, ".sync-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
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
	if e = os.Rename(f.Name(), filepath.Join(s.dir, name)); e != nil {
		return e
	}
	return s.sync()
}
func (s *WGSyncStore) sync() error {
	d, e := os.Open(s.dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (s *WGSyncStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("sync store closed")
	}
	e := os.Remove(filepath.Join(s.dir, "manifest.json"))
	if e != nil && !os.IsNotExist(e) {
		return errors.New("could not invalidate snapshot")
	}
	return s.sync()
}
func (s *WGSyncStore) Publish(l nodewire.WGLease, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return errors.New("sync store closed")
	}
	raw, e := json.Marshal(l)
	if e != nil {
		return e
	}
	if _, e = nodewire.ParseWGLease(raw, s.identity.NodeID, now); e != nil {
		return e
	}
	return s.atomic("manifest.json", raw)
}

// ManifestPath 返回租约快照的落盘路径，供 observer 与 applier 读取；
// 文件名只在本包内定义，调用方不应自行拼接。
func (s *WGSyncStore) ManifestPath() string { return filepath.Join(s.dir, "manifest.json") }
