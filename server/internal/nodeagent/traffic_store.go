package nodeagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/yuhai-vpn/server/internal/nodewire"
)

// 流量上报的持久状态。与心跳状态目录同样的做法：独占文件锁、原子写、身份绑定。
//
// 这里最关键的一条是**基线只在上报被确认后才前移**：
//   - 未确认就前移 → 这段流量永久丢失，用户白用；
//   - 确认前重复发送同一个 report_id → 服务端按 (node_id, report_id) 幂等去重，不会重复计费。
//
// 因此 pending 与 baseline 必须同时落盘、同时替换，任何中断都只会退回到「重发同一份」。
type TrafficPending struct {
	ReportID    string                 `json:"report_id"`
	WindowStart string                 `json:"window_start"`
	WindowEnd   string                 `json:"window_end"`
	Items       []nodewire.TrafficItem `json:"items"`
	Snapshot    []TrafficCounter       `json:"snapshot"`
}

type TrafficState struct {
	Schema   int              `json:"schema"`
	NodeID   string           `json:"node_id"`
	Endpoint string           `json:"endpoint"`
	Baseline []TrafficCounter `json:"baseline"`
	// 上次已确认上报的窗口结束时间，作为下一次窗口的起点；为空表示尚未上报过。
	LastWindowEnd string          `json:"last_window_end"`
	Pending       *TrafficPending `json:"pending"`
}

type TrafficStore struct {
	mu    sync.Mutex
	dir   string
	state TrafficState
	lock  *os.File
}

const trafficStateFile = "traffic.json"

func trafficStoreError() error { return errors.New("流量上报状态不可用") }

func InitTrafficStore(dir, nodeID, origin string) error {
	endpoint, err := Endpoint(origin)
	if err != nil {
		return err
	}
	n, err := integer(nodeID)
	if err != nil || n == 0 || !filepath.IsAbs(dir) {
		return errors.New("流量上报身份无效")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return errors.New("流量上报初始化需要新目录")
	}
	s := &TrafficStore{dir: dir, state: TrafficState{Schema: 1, NodeID: nodeID, Endpoint: endpoint, Baseline: []TrafficCounter{}}}
	return s.persist()
}

func OpenTrafficStore(dir, nodeID, origin string) (*TrafficStore, error) {
	endpoint, err := Endpoint(origin)
	if err != nil {
		return nil, err
	}
	if err := checkRuntimeDir(dir); err != nil {
		return nil, err
	}
	f, err := lockState(filepath.Join(dir, "traffic.lock"))
	if err != nil {
		return nil, err
	}
	s := &TrafficStore{dir: dir, lock: f}
	raw, err := readPrivate(filepath.Join(dir, trafficStateFile), 4194304)
	if err != nil {
		s.Close()
		return nil, trafficStoreError()
	}
	var state TrafficState
	if strictJSON(raw, &state) != nil || state.Schema != 1 || state.NodeID != nodeID || state.Endpoint != endpoint {
		s.Close()
		return nil, errors.New("流量上报身份不匹配或状态损坏")
	}
	if state.Baseline == nil {
		s.Close()
		return nil, trafficStoreError()
	}
	s.state = state
	return s, nil
}

func (s *TrafficStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
}

func (s *TrafficStore) persist() error {
	raw, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".traffic-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(s.dir, trafficStateFile)); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Pending 返回尚未确认的上报；没有则返回 nil。
func (s *TrafficStore) Pending() *TrafficPending {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Pending == nil {
		return nil
	}
	copied := *s.state.Pending
	return &copied
}

// LastWindowEnd 返回上次已确认上报的窗口结束时间；为空表示尚未上报过。
func (s *TrafficStore) LastWindowEnd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.LastWindowEnd
}

// Baseline 返回上次已确认时的累计计数器。
func (s *TrafficStore) Baseline() []TrafficCounter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TrafficCounter(nil), s.state.Baseline...)
}

// Stage 落盘一份待确认的上报。已有待确认上报时拒绝覆盖——必须先把它送达，
// 否则那一段流量会被静默丢弃。
func (s *TrafficStore) Stage(p TrafficPending) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return trafficStoreError()
	}
	if s.state.Pending != nil {
		return errors.New("已有待确认的流量上报")
	}
	if !nodewire.ValidTrafficReportID(p.ReportID) || p.Snapshot == nil || p.Items == nil {
		return trafficStoreError()
	}
	s.state.Pending = &p
	if err := s.persist(); err != nil {
		s.state.Pending = nil
		return err
	}
	return nil
}

// Commit 在控制面确认后前移基线并清除待确认上报。两者同一次落盘，不会出现
// 「基线已前移但 pending 还在」这种会导致重复计费的中间态。
func (s *TrafficStore) Commit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return trafficStoreError()
	}
	if s.state.Pending == nil {
		return nil
	}
	previous, pending, window := s.state.Baseline, s.state.Pending, s.state.LastWindowEnd
	s.state.Baseline, s.state.LastWindowEnd, s.state.Pending = pending.Snapshot, pending.WindowEnd, nil
	if err := s.persist(); err != nil {
		s.state.Baseline, s.state.LastWindowEnd, s.state.Pending = previous, window, pending
		return err
	}
	return nil
}
