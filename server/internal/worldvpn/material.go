package worldvpn

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

// MonthsForDays 把宇海时长套餐的天数折成批发接口的月数。
// 只接受月卡 / 季卡 / 年卡附近的天数，避免把别的时长买成错误的月数。
func MonthsForDays(days int) (int, bool) {
	switch {
	case days >= 28 && days <= 31:
		return 1, true
	case days >= 89 && days <= 92:
		return 3, true
	case days >= 360 && days <= 366:
		return 12, true
	default:
		return 0, false
	}
}

func PlanCodeForMonths(months int) (string, bool) {
	switch months {
	case 1:
		return "worldvpn-m1", true
	case 3:
		return "worldvpn-m3", true
	case 12:
		return "worldvpn-m12", true
	default:
		return "", false
	}
}

type wireGuardMaterial struct {
	PrivateKey      string
	Address         string
	DNS             []string
	ServerPublicKey string
	Endpoint        string
	MTU             int
}

func parseWireGuardConfig(text string) (wireGuardMaterial, error) {
	var m wireGuardMaterial
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return wireGuardMaterial{}, errConfig
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch section + "." + key {
		case "interface.privatekey":
			m.PrivateKey = val
		case "interface.address":
			m.Address = strings.TrimSpace(strings.Split(val, ",")[0])
		case "interface.dns":
			m.DNS = splitDNS(val)
		case "interface.mtu":
			n, err := strconv.Atoi(val)
			if err != nil {
				return wireGuardMaterial{}, errConfig
			}
			m.MTU = n
		case "peer.publickey":
			m.ServerPublicKey = val
		case "peer.endpoint":
			m.Endpoint = val
		}
	}
	if sc.Err() != nil || m.PrivateKey == "" || m.Address == "" || m.ServerPublicKey == "" || m.Endpoint == "" || len(m.DNS) == 0 {
		return wireGuardMaterial{}, errConfig
	}
	if m.PrivateKey == m.ServerPublicKey || !wgKey(m.PrivateKey) || !wgKey(m.ServerPublicKey) {
		return wireGuardMaterial{}, errConfig
	}
	prefix, err := netip.ParsePrefix(m.Address)
	if err != nil || !prefix.IsValid() || prefix.Bits() != 32 || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() {
		return wireGuardMaterial{}, errConfig
	}
	m.Address = prefix.String()
	host, port, err := net.SplitHostPort(m.Endpoint)
	if err != nil || !validEndpointHost(host) {
		return wireGuardMaterial{}, errConfig
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return wireGuardMaterial{}, errConfig
	}
	for _, d := range m.DNS {
		ip, err := netip.ParseAddr(d)
		if err != nil || !ip.IsValid() || ip.IsMulticast() {
			return wireGuardMaterial{}, errConfig
		}
	}
	if m.MTU == 0 {
		m.MTU = 1280
	}
	if m.MTU < 1280 || m.MTU > 1500 {
		return wireGuardMaterial{}, errConfig
	}
	return m, nil
}

var errConfig = &APIError{Action: "getWireGuardConfig", Message: "配置内容不完整"}

func splitDNS(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func encodeB64(b []byte) string          { return base64.StdEncoding.EncodeToString(b) }

func wgKey(s string) bool {
	if len(s) != 44 || strings.ContainsAny(s, "\r\n") {
		return false
	}
	raw, err := decodeB64(s)
	if err != nil || len(raw) != 32 {
		return false
	}
	zero := true
	for _, b := range raw {
		if b != 0 {
			zero = false
			break
		}
	}
	return !zero && encodeB64(raw) == s
}

func validEndpointHost(host string) bool {
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n/@") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLoopback()
	}
	if !strings.Contains(host, ".") || isAllDigitsAndDots(host) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
				return false
			}
		}
	}
	return true
}

func isAllDigitsAndDots(s string) bool {
	for _, r := range s {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func parseServers(raw any) ([]serverLine, error) {
	rows, ok := raw.([]any)
	if !ok {
		return nil, &APIError{Action: "getServers", Message: "缺少 servers"}
	}
	if len(rows) > 1000 {
		return nil, &APIError{Action: "getServers", Message: "线路过多"}
	}
	out := make([]serverLine, 0, len(rows))
	seen := map[int64]bool{}
	for _, item := range rows {
		row, ok := item.([]any)
		if !ok || len(row) < 3 {
			return nil, &APIError{Action: "getServers", Message: "线路行格式不对"}
		}
		id, ok := asID(row[0])
		name, okName := row[1].(string)
		ip, okIP := row[2].(string)
		if !ok || !okName || !okIP || id <= 0 || seen[id] {
			return nil, &APIError{Action: "getServers", Message: "线路行缺字段"}
		}
		name = cleanName(name)
		if name == "" || !publicIP(ip) {
			return nil, &APIError{Action: "getServers", Message: "线路行缺字段"}
		}
		seen[id] = true
		out = append(out, serverLine{ID: id, Name: name, IP: ip})
	}
	return out, nil
}

func asID(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) || n <= 0 || n > float64(1<<62) {
			return 0, false
		}
		return int64(n), true
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	default:
		return 0, false
	}
}

func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if len([]rune(name)) > 64 {
		rs := []rune(name)
		name = string(rs[:64])
	}
	return name
}

func publicIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLoopback() || ip.IsPrivate() {
		return false
	}
	return true
}

// PlaceOf 从线路名字里猜国家和大区，只用于客户端分组展示。猜不到就用 ZZ / OTHER。
func PlaceOf(name string) (country, region string) {
	n := strings.ToLower(name)
	for _, p := range places {
		if strings.Contains(n, p.needle) {
			return p.country, p.region
		}
	}
	return "ZZ", "OTHER"
}

var places = []struct{ needle, country, region string }{
	{"australia", "AU", "OCEANIA"},
	{"japan", "JP", "ASIA"},
	{"singapore", "SG", "ASIA"},
	{"hong kong", "HK", "ASIA"},
	{"taiwan", "TW", "ASIA"},
	{"korea", "KR", "ASIA"},
	{"germany", "DE", "EUROPE"},
	{"france", "FR", "EUROPE"},
	{"netherlands", "NL", "EUROPE"},
	{"united kingdom", "GB", "EUROPE"},
	{"london", "GB", "EUROPE"},
	{"united states", "US", "AMERICAS"},
	{"new york", "US", "AMERICAS"},
	{"los angeles", "US", "AMERICAS"},
	{"canada", "CA", "AMERICAS"},
	{"brazil", "BR", "AMERICAS"},
}

// ProbePort 只给客户端做 TCP 延迟参考。listServers 不返回端口；真正的 Endpoint 以配置文件为准。
const ProbePort = 51820
