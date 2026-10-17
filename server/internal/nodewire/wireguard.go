package nodewire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
)

// WGManifest contains public desired state only. Reading it does not apply it.
type WGManifest struct {
	Schema        int      `json:"schema"`
	NodeID        string   `json:"node_id"`
	ConfigVersion string   `json:"config_version"`
	Interface     string   `json:"interface"`
	PublicKey     string   `json:"public_key"`
	ListenPort    int      `json:"listen_port"`
	Peers         []WGPeer `json:"peers"`
}
type WGPeer struct {
	CredentialID string   `json:"credential_id"`
	PublicKey    string   `json:"public_key"`
	AllowedIPs   []string `json:"allowed_ips"`
}

var wgInterface = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func validWGInterface(s string) bool {
	return wgInterface.MatchString(s) && s != "lo" && s != "all" && s != "interfaces"
}
func validPublicKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != s {
		return false
	}
	var v byte
	for _, c := range b {
		v |= c
	}
	return v != 0
}
func validPeerIP(s string) bool {
	p, err := netip.ParsePrefix(s)
	if err != nil || p.String() != s || p.Addr().Is4In6() || !p.Addr().IsPrivate() {
		return false
	}
	return p.Bits() == p.Addr().BitLen()
}
func ParseWGManifest(raw []byte, nodeID string) (WGManifest, string, error) {
	var m WGManifest
	bad := errors.New("invalid WireGuard public manifest")
	if len(raw) > 1048576 || StrictJSON(raw, &m) != nil || m.Schema != 1 || m.NodeID != nodeID || !validWGInterface(m.Interface) || !validPublicKey(m.PublicKey) || m.ListenPort < 1 || m.ListenPort > 65535 || m.Peers == nil || len(m.Peers) > 1000 {
		return m, "", bad
	}
	for _, v := range []string{m.NodeID, m.ConfigVersion} {
		n, e := wgInteger(v)
		if e != nil || n == 0 {
			return m, "", bad
		}
	}
	ids, keys, ips := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range m.Peers {
		id, e := wgInteger(p.CredentialID)
		if e != nil || id == 0 || ids[p.CredentialID] || keys[p.PublicKey] || p.PublicKey == m.PublicKey || !validPublicKey(p.PublicKey) || len(p.AllowedIPs) < 1 || len(p.AllowedIPs) > 2 {
			return m, "", bad
		}
		ids[p.CredentialID] = true
		keys[p.PublicKey] = true
		for _, ip := range p.AllowedIPs {
			if !validPeerIP(ip) || ips[ip] {
				return m, "", bad
			}
			ips[ip] = true
		}
		sort.Strings(m.Peers[i].AllowedIPs)
	}
	sort.Slice(m.Peers, func(i, j int) bool {
		a, _ := wgInteger(m.Peers[i].CredentialID)
		b, _ := wgInteger(m.Peers[j].CredentialID)
		return a < b
	})
	canonical, _ := json.Marshal(m.Peers)
	digest := sha256.Sum256(canonical)
	return m, hex.EncodeToString(digest[:]), nil
}

func wgInteger(s string) (int64, error) {
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != s {
		return 0, errors.New("invalid integer")
	}
	return n, nil
}

func ValidWGInterface(s string) bool { return validWGInterface(s) }

// 下发侧（nodeagent）在构造命令前需要用与解析侧完全相同的规则再校验一次，
// 因此这里导出同一份实现，避免两边各写一套后规则漂移。
func ValidWGPublicKey(s string) bool { return validPublicKey(s) }

func ValidWGPeerIP(s string) bool { return validPeerIP(s) }
