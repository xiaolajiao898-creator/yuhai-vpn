package nodewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"
)

type Heartbeat struct {
	ReadyProtocols   []string `json:"ready_protocols,omitempty"`
	Sequence         string   `json:"sequence"`
	SentAt           string   `json:"sent_at"`
	AgentVersion     string   `json:"agent_version"`
	ConfigVersion    string   `json:"config_version"`
	SnapshotChecksum string   `json:"snapshot_checksum"`
	OnlineCount      int64    `json:"online_count"`
	CPU              int      `json:"cpu"`
	Memory           int      `json:"mem"`
	NetRX            string   `json:"net_rx_bps"`
	NetTX            string   `json:"net_tx_bps"`
	Kernel           string   `json:"kernel,omitempty"`
}

type Receipt struct {
	Accepted   bool      `json:"accepted"`
	Duplicate  bool      `json:"duplicate"`
	Sequence   string    `json:"sequence"`
	ReceivedAt time.Time `json:"received_at"`
	ServerTime time.Time `json:"server_time"`
	Actions    []string  `json:"actions"`
}

var (
	numberPattern    = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
	versionPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$`)
	checksumPattern  = regexp.MustCompile(`^([a-f0-9]{64})?$`)
	kernelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+ /()-]{0,63}$`)
	timestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?Z$`)
)

func decimal(s string) (int64, error) {
	if !numberPattern.MatchString(s) {
		return 0, badHeartbeat()
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, badHeartbeat()
	}
	return n, nil
}

func badHeartbeat() error { return errors.New("invalid heartbeat") }

type Values struct {
	Sequence, Version, RX, TX int64
	Sent                      time.Time
	Hash                      string
}

// ValidateHeartbeat is shared by the sending agent and the receiving handler.
func ValidateHeartbeat(h Heartbeat) error { _, err := Validate(h); return err }

func ValidProtocols(protocols []string) bool {
	if len(protocols) > 5 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range protocols {
		switch p {
		case "VLESS_REALITY", "TROJAN", "SHADOWSOCKS_2022", "WIREGUARD", "HYSTERIA2":
		default:
			return false
		}
		if seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

func Validate(h Heartbeat) (Values, error) {
	var v Values
	var err error
	if !ValidProtocols(h.ReadyProtocols) {
		return v, badHeartbeat()
	}
	if v.Sequence, err = decimal(h.Sequence); err != nil || v.Sequence == 0 {
		return v, badHeartbeat()
	}
	if v.Version, err = decimal(h.ConfigVersion); err != nil {
		return v, err
	}
	if v.RX, err = decimal(h.NetRX); err != nil {
		return v, err
	}
	if v.TX, err = decimal(h.NetTX); err != nil {
		return v, err
	}
	if !timestampPattern.MatchString(h.SentAt) {
		return v, badHeartbeat()
	}
	if v.Sent, err = time.Parse(time.RFC3339Nano, h.SentAt); err != nil || v.Sent.Year() < 2020 || v.Sent.Year() > 9999 {
		return v, badHeartbeat()
	}
	if !versionPattern.MatchString(h.AgentVersion) || !checksumPattern.MatchString(h.SnapshotChecksum) || h.CPU < 0 || h.CPU > 100 || h.Memory < 0 || h.Memory > 100 || h.OnlineCount < 0 || h.OnlineCount > 10000000 || (h.Kernel != "" && !kernelPattern.MatchString(h.Kernel)) {
		return v, badHeartbeat()
	}
	if len(h.ReadyProtocols) > 0 && (v.Version == 0 || len(h.SnapshotChecksum) != 64) {
		return v, badHeartbeat()
	}
	h.SentAt = v.Sent.Format(time.RFC3339Nano)
	b, err := json.Marshal(h)
	if err != nil {
		return v, err
	}
	digest := sha256.Sum256(b)
	v.Hash = hex.EncodeToString(digest[:])
	return v, nil
}
