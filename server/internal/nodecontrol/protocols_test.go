package nodecontrol

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatReadyProtocolsStrictAndLegacy(t *testing.T) {
	c := newClock()
	h := heartbeatAt(c, "1")
	raw, _ := json.Marshal(h)
	decode := func(b []byte) error {
		r := httptest.NewRequest("POST", "https://node.test/node/v1/heartbeat", strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
		_, e := decodeHeartbeat(r, httptest.NewRecorder())
		return e
	}
	if e := decode(raw); e != nil {
		t.Fatal("legacy heartbeat rejected", e)
	}
	h.ReadyProtocols = []string{"WIREGUARD"}
	if _, e := validate(h); e == nil {
		t.Fatal("ready without configuration")
	}
	h.ConfigVersion = "1"
	h.SnapshotChecksum = strings.Repeat("b", 64)
	good, _ := json.Marshal(h)
	if e := decode(good); e != nil {
		t.Fatal(e)
	}
	for _, replace := range []string{`null`, `["WIREGUARD","WIREGUARD"]`, `["MOCK"]`, `[1]`, `["wireguard"]`} {
		bad := strings.Replace(string(good), `["WIREGUARD"]`, replace, 1)
		if e := decode([]byte(bad)); e == nil {
			t.Fatal("bad ready protocols", replace)
		}
	}
	bad := strings.Replace(string(good), `"ready_protocols"`, `"READY_PROTOCOLS"`, 1)
	if e := decode([]byte(bad)); e == nil {
		t.Fatal("case alias")
	}
}
func TestReadyProtocolsMigrationAndReplayIntegration(t *testing.T) {
	f := newHeartbeatFixture(t)
	h := heartbeatAt(f.clock, "1")
	if _, e := f.accept(h); e != nil {
		t.Fatal(e)
	}
	migration, e := os.ReadFile("../db/migrations/00020_node_ready_protocols.sql")
	if e != nil {
		t.Fatal(e)
	}
	parts := strings.Split(string(migration), "-- +goose Down")
	f.exec(t, parts[1])
	f.exec(t, parts[0])
	var protocols []string
	var seq int64
	if e = f.owner.QueryRow(f.ctx, `SELECT sequence,ready_protocols FROM node_heartbeats`).Scan(&seq, &protocols); e != nil || seq != 1 || len(protocols) != 0 {
		t.Fatal("migration invented readiness", seq, protocols, e)
	}
	if r, e := f.accept(h); e != nil || !r.Duplicate {
		t.Fatal("legacy pending request changed", e)
	}
	h.ReadyProtocols = []string{"WIREGUARD"}
	h.ConfigVersion = "1"
	h.SnapshotChecksum = strings.Repeat("b", 64)
	if _, e = f.accept(h); e == nil {
		t.Fatal("same sequence authorized new protocols")
	}
	f.clock.Add(30 * time.Second)
	h.Sequence = "2"
	h.SentAt = f.clock.Now().Format(time.RFC3339Nano)
	if _, e = f.accept(h); e != nil {
		t.Fatal(e)
	}
	if e = f.owner.QueryRow(f.ctx, `SELECT sequence,ready_protocols FROM node_heartbeats`).Scan(&seq, &protocols); e != nil || seq != 2 || strings.Join(protocols, ",") != "WIREGUARD" {
		t.Fatal("ready set not atomic", e)
	}
}
