package egress

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// 导入上限：防止一次粘贴拖垮后台。
const (
	MaxInputBytes = 1 << 20
	MaxRows       = 5000
)

type ParseOptions struct {
	// DefaultProtocol 用于没有协议前缀的「IP:端口:账号:密码」格式：SOCKS5 / HTTP / HTTPS。
	DefaultProtocol string
	// DefaultUDP 表示 SOCKS5 代理端是否支持 UDP ASSOCIATE（管理员按供应商说明勾选）。
	DefaultUDP  bool
	CountryCode string
	City        string
	Region      string
	NamePrefix  string
	// AllowPrivate 仅供本机隔离测试：允许 127.0.0.1 / 内网地址作为出口。后台导入从不设置。
	AllowPrivate bool
}

// Row 是预览中的一行。Source 永远是脱敏后的片段，不含任何凭据。
type Row struct {
	Line      int       `json:"line"`
	Source    string    `json:"source"`
	Endpoint  *Endpoint `json:"-"`
	Error     string    `json:"error,omitempty"`
	Duplicate bool      `json:"duplicate,omitempty"`
}

// Parse 识别输入格式（JSON / CSV / base64 订阅 / 逐行 URI 或冒号格式）并逐行解析。
// 返回的每一行要么有 Endpoint（已通过 Validate），要么有面向管理员的 Error。
func Parse(input string, opts ParseOptions) ([]Row, error) {
	if len(input) > MaxInputBytes {
		return nil, fmt.Errorf("输入超过 1MB")
	}
	switch opts.DefaultProtocol {
	case "":
		opts.DefaultProtocol = SOCKS5
	case SOCKS5, HTTP, HTTPS:
	default:
		return nil, fmt.Errorf("默认协议只能是 SOCKS5 / HTTP / HTTPS")
	}
	text := strings.TrimPrefix(strings.TrimSpace(input), "\uFEFF")
	if text == "" {
		return nil, fmt.Errorf("没有可导入的内容")
	}
	var rows []Row
	var err error
	switch {
	case (strings.HasPrefix(text, "[") || strings.HasPrefix(text, "{")) && json.Valid([]byte(text)):
		rows, err = parseJSON(text, opts)
	case looksBase64(text):
		decoded, derr := decodeB64(strings.Join(strings.Fields(text), ""))
		if derr != nil || !strings.Contains(decoded, "://") {
			return nil, fmt.Errorf("无法识别的订阅内容")
		}
		rows, err = parseLines(decoded, opts)
	case isCSV(text):
		rows, err = parseCSV(text, opts)
	default:
		rows, err = parseLines(text, opts)
	}
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxRows {
		return nil, fmt.Errorf("一次最多导入 %d 行", MaxRows)
	}
	seen := map[string]int{}
	for i := range rows {
		r := &rows[i]
		if r.Endpoint == nil {
			continue
		}
		applyDefaults(r.Endpoint, opts)
		if verr := r.Endpoint.Validate(); verr != nil {
			r.Error, r.Endpoint = verr.Error(), nil
			continue
		}
		if !opts.AllowPrivate && !PublicHost(r.Endpoint.Host) {
			r.Error, r.Endpoint = "出口地址是本机或内网地址，用户设备无法连接", nil
			continue
		}
		r.Source = r.Endpoint.Display()
		fp := r.Endpoint.Fingerprint()
		if first, ok := seen[fp]; ok {
			r.Duplicate = true
			r.Error = fmt.Sprintf("与第 %d 行重复", first)
			continue
		}
		seen[fp] = r.Line
	}
	return rows, nil
}

func applyDefaults(e *Endpoint, o ParseOptions) {
	if e.CountryCode == "" {
		e.CountryCode = o.CountryCode
	}
	if e.City == "" {
		e.City = o.City
	}
	if e.Region == "" {
		e.Region = o.Region
	}
	if e.Name == "" && o.NamePrefix != "" {
		e.Name = o.NamePrefix
	}
}

// Display 是脱敏展示串：协议://身份提示@主机:端口。
func (e *Endpoint) Display() string {
	id := e.MaskedIdentity()
	if id != "" {
		id += "@"
	}
	return strings.ToLower(e.Protocol) + "://" + id + hostPort(e.Host, e.Port)
}

func hostPort(h string, p int) string {
	if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(p)
}

// maskedSource 用于解析失败的行：只露出协议前缀与长度，绝不回显原文（原文可能含密码）。
func maskedSource(line string) string {
	if i := strings.Index(line, "://"); i > 0 && i < 16 {
		return line[:i+3] + fmt.Sprintf("…（%d 字符）", len(line))
	}
	return fmt.Sprintf("（%d 字符）", len(line))
}

func looksBase64(s string) bool {
	s = strings.Join(strings.Fields(s), "")
	if len(s) < 16 || strings.Contains(s, "://") || strings.Contains(s, ":") {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func decodeB64(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("base64 解码失败")
}

func isCSV(text string) bool {
	first := strings.ToLower(strings.SplitN(text, "\n", 2)[0])
	return strings.Contains(first, ",") && strings.Contains(first, "host") && strings.Contains(first, "port")
}

func parseLines(text string, o ParseOptions) ([]Row, error) {
	var rows []Row
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if len(rows) >= MaxRows {
			return nil, fmt.Errorf("一次最多导入 %d 行", MaxRows)
		}
		row := Row{Line: i + 1, Source: maskedSource(line)}
		e, err := parseOne(line, o)
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Endpoint = e
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// parseOne 解析一行：URI 或冒号格式。
func parseOne(line string, o ParseOptions) (*Endpoint, error) {
	if i := strings.Index(line, "://"); i > 0 {
		return parseURI(line)
	}
	return parseColon(line, o)
}

// parseColon 支持 host:port、host:port:user:pass、user:pass@host:port（IPv6 需带方括号）。
func parseColon(line string, o ParseOptions) (*Endpoint, error) {
	e := &Endpoint{Protocol: o.DefaultProtocol, SupportsUDP: o.DefaultUDP && o.DefaultProtocol == SOCKS5,
		Params: map[string]string{}, Secret: map[string]string{}}
	if o.DefaultProtocol == HTTPS {
		e.Params["security"] = "tls"
	}
	var hostport, user, pass string
	at := strings.LastIndex(line, "@")
	if at > 0 {
		// 只有 @ 之后确实是「主机:端口」才按 user:pass@host:port 解析；
		// 否则 @ 属于密码的一部分（如 IP:端口:账号:p@ss）。
		if _, _, err := splitHostPort(line[at+1:]); err != nil || !strings.Contains(line[:at], ":") {
			at = -1
		}
	}
	if at > 0 {
		cred := line[:at]
		hostport = line[at+1:]
		c := strings.SplitN(cred, ":", 2)
		if len(c) != 2 {
			return nil, fmt.Errorf("账号密码格式应为 user:pass@host:port")
		}
		user, pass = c[0], c[1]
		h, p, err := splitHostPort(hostport)
		if err != nil {
			return nil, err
		}
		e.Host, e.Port = h, p
	} else {
		var host, rest string
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				return nil, fmt.Errorf("IPv6 地址缺少右方括号")
			}
			host, rest = line[1:end], strings.TrimPrefix(line[end+1:], ":")
		} else {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("缺少端口，格式应为 IP:端口 或 IP:端口:账号:密码")
			}
			host, rest = parts[0], parts[1]
		}
		fields := strings.SplitN(rest, ":", 3)
		port, err := parsePort(fields[0])
		if err != nil {
			return nil, err
		}
		e.Host, e.Port = host, port
		switch len(fields) {
		case 1:
		case 3:
			user, pass = fields[1], fields[2]
		default:
			return nil, fmt.Errorf("格式应为 IP:端口:账号:密码")
		}
	}
	if user != "" || pass != "" {
		e.Secret["user"], e.Secret["pass"] = user, pass
	}
	if e.Protocol == HTTPS {
		e.Params["sni"] = sniDefault(e.Host)
	}
	return e, nil
}

func sniDefault(host string) string {
	if _, err := NormalizeHost(host); err == nil && !strings.ContainsAny(host, ":") && !isIPv4(host) {
		return strings.ToLower(host)
	}
	return ""
}

func isIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("端口无效")
	}
	return p, nil
}

func splitHostPort(hp string) (string, int, error) {
	hp = strings.TrimSpace(hp)
	var host, port string
	if strings.HasPrefix(hp, "[") {
		end := strings.Index(hp, "]")
		if end < 0 || !strings.HasPrefix(hp[end+1:], ":") {
			return "", 0, fmt.Errorf("IPv6 地址格式应为 [地址]:端口")
		}
		host, port = hp[1:end], hp[end+2:]
	} else {
		i := strings.LastIndex(hp, ":")
		if i <= 0 {
			return "", 0, fmt.Errorf("缺少端口")
		}
		host, port = hp[:i], hp[i+1:]
	}
	p, err := parsePort(port)
	return host, p, err
}

// insecure 检查分享链接里「关闭 TLS 校验」的各种写法。
func insecure(q url.Values) bool {
	for _, k := range []string{"allowInsecure", "allow_insecure", "insecure", "skip-cert-verify", "allowinsecure"} {
		switch strings.ToLower(q.Get(k)) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

var errInsecure = fmt.Errorf("不允许关闭 TLS 证书校验（如需自签证书，请改用证书指纹 pin_sha256）")

func fragmentName(u *url.URL) string {
	if u.Fragment == "" {
		return ""
	}
	if n, err := url.PathUnescape(u.Fragment); err == nil {
		return strings.TrimSpace(n)
	}
	return strings.TrimSpace(u.Fragment)
}

func parseURI(line string) (*Endpoint, error) {
	scheme := strings.ToLower(line[:strings.Index(line, "://")])
	switch scheme {
	case "vmess":
		return parseVMess(line[len("vmess://"):])
	case "ss":
		return parseSS(line)
	case "tuic":
		return nil, fmt.Errorf("%s", UnsupportedReason(TUIC))
	case "wireguard", "wg":
		return nil, fmt.Errorf("WireGuard 请走自建节点流程登记，不能作为外部代理导入")
	}
	u, err := url.Parse(line)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("链接格式无效")
	}
	q := u.Query()
	if insecure(q) {
		return nil, errInsecure
	}
	host := u.Hostname()
	port, err := parsePort(u.Port())
	if err != nil {
		return nil, err
	}
	e := &Endpoint{Host: host, Port: port, Name: fragmentName(u), Params: map[string]string{}, Secret: map[string]string{}}
	user := u.User.Username()
	pass, hasPass := u.User.Password()
	switch scheme {
	case "socks", "socks5", "socks5h":
		e.Protocol = SOCKS5
		if scheme == "socks" && user != "" && !hasPass {
			// v2rayN 风格：socks://base64(user:pass)@host:port
			if d, derr := decodeB64(user); derr == nil && strings.Contains(d, ":") {
				c := strings.SplitN(d, ":", 2)
				user, pass = c[0], c[1]
			}
		}
		e.SupportsUDP = q.Get("udp") == "1" || strings.EqualFold(q.Get("udp"), "true")
		e.Secret["user"], e.Secret["pass"] = user, pass
	case "http":
		e.Protocol = HTTP
		e.Secret["user"], e.Secret["pass"] = user, pass
	case "https":
		e.Protocol = HTTPS
		e.Secret["user"], e.Secret["pass"] = user, pass
		e.Params["security"] = "tls"
		e.Params["sni"] = firstNonEmpty(q.Get("sni"), sniDefault(host))
	case "trojan":
		e.Protocol = Trojan
		e.Secret["password"] = user
		if hasPass {
			e.Secret["password"] = user + ":" + pass
		}
		transport(e, q)
		e.Params["security"] = "tls"
		e.Params["sni"] = firstNonEmpty(q.Get("sni"), q.Get("peer"), sniDefault(host))
	case "vless":
		e.Protocol = VLESS
		e.Secret["uuid"] = user
		if enc := q.Get("encryption"); enc != "" && enc != "none" {
			return nil, fmt.Errorf("VLESS encryption=%s 暂不支持", enc)
		}
		transport(e, q)
		e.Params["security"] = strings.ToLower(firstNonEmpty(q.Get("security"), "none"))
		e.Params["sni"] = q.Get("sni")
		e.Params["pbk"] = q.Get("pbk")
		e.Params["sid"] = q.Get("sid")
		e.Params["spx"] = q.Get("spx")
		e.Params["flow"] = q.Get("flow")
		if e.Params["security"] == "tls" && e.Params["sni"] == "" {
			e.Params["sni"] = sniDefault(host)
		}
	case "hysteria2", "hy2":
		e.Protocol = Hysteria2
		auth := user
		if hasPass {
			auth = user + ":" + pass
		}
		e.Secret["password"] = auth
		e.Params["sni"] = firstNonEmpty(q.Get("sni"), sniDefault(host))
		e.Params["obfs"] = q.Get("obfs")
		if pin := firstNonEmpty(q.Get("pinSHA256"), q.Get("pinsha256")); pin != "" {
			e.Params["pin_sha256"] = pin
		}
		if a := q.Get("alpn"); a != "" {
			e.Params["alpn"] = a
		}
	default:
		return nil, fmt.Errorf("无法识别协议 %s", scheme)
	}
	if fp := q.Get("fp"); fp != "" {
		e.Params["fp"] = strings.ToLower(fp)
	}
	if pin := q.Get("pin_sha256"); pin != "" {
		e.Params["pin_sha256"] = pin
	}
	return e, nil
}

func transport(e *Endpoint, q url.Values) {
	e.Params["network"] = strings.ToLower(firstNonEmpty(q.Get("type"), q.Get("network")))
	if e.Params["network"] == "tcp" || e.Params["network"] == "raw" {
		e.Params["network"] = ""
	}
	e.Params["path"] = q.Get("path")
	e.Params["host_header"] = q.Get("host")
	e.Params["service_name"] = q.Get("serviceName")
	e.Params["mode"] = q.Get("mode")
	if a := q.Get("alpn"); a != "" {
		e.Params["alpn"] = a
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// parseSS 支持 SIP002（ss://base64url(method:password)@host:port#name 或明文 method:password）
// 与旧格式 ss://base64(method:password@host:port)#name。带 plugin 的一律拒绝。
func parseSS(line string) (*Endpoint, error) {
	rest := line[len("ss://"):]
	name := ""
	if i := strings.Index(rest, "#"); i >= 0 {
		name, _ = url.PathUnescape(rest[i+1:])
		rest = rest[:i]
	}
	if i := strings.Index(rest, "?"); i >= 0 {
		q, _ := url.ParseQuery(rest[i+1:])
		if q.Get("plugin") != "" {
			return nil, fmt.Errorf("Shadowsocks 插件（plugin）暂不支持")
		}
		rest = rest[:i]
	}
	rest = strings.TrimSuffix(rest, "/")
	var userinfo, hostport string
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		userinfo, hostport = rest[:at], rest[at+1:]
		if d, err := decodeB64(userinfo); err == nil && strings.Contains(d, ":") {
			userinfo = d
		} else if u, err := url.PathUnescape(userinfo); err == nil {
			userinfo = u
		}
	} else {
		d, err := decodeB64(rest)
		if err != nil {
			return nil, fmt.Errorf("ss 链接格式无效")
		}
		at := strings.LastIndex(d, "@")
		if at < 0 {
			return nil, fmt.Errorf("ss 链接格式无效")
		}
		userinfo, hostport = d[:at], d[at+1:]
	}
	c := strings.SplitN(userinfo, ":", 2)
	if len(c) != 2 {
		return nil, fmt.Errorf("ss 链接缺少加密方式或密码")
	}
	host, port, err := splitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	return &Endpoint{Protocol: Shadowsocks, Host: host, Port: port, Name: strings.TrimSpace(name),
		Params: map[string]string{"method": strings.ToLower(c[0])}, Secret: map[string]string{"password": c[1]}}, nil
}

type vmessShare struct {
	PS   string          `json:"ps"`
	Add  string          `json:"add"`
	Port json.RawMessage `json:"port"`
	ID   string          `json:"id"`
	Aid  json.RawMessage `json:"aid"`
	Scy  string          `json:"scy"`
	Net  string          `json:"net"`
	Type string          `json:"type"`
	Host string          `json:"host"`
	Path string          `json:"path"`
	TLS  string          `json:"tls"`
	SNI  string          `json:"sni"`
	ALPN string          `json:"alpn"`
	FP   string          `json:"fp"`
	// 各种「关闭证书校验」写法
	Insecure      json.RawMessage `json:"allowInsecure"`
	SkipVerify    json.RawMessage `json:"skip-cert-verify"`
	InsecureShort json.RawMessage `json:"insecure"`
}

func rawTrue(r json.RawMessage) bool {
	s := strings.Trim(strings.ToLower(string(r)), `" `)
	return s == "1" || s == "true"
}

func parseVMess(b64 string) (*Endpoint, error) {
	if i := strings.Index(b64, "#"); i >= 0 {
		b64 = b64[:i]
	}
	d, err := decodeB64(b64)
	if err != nil {
		return nil, fmt.Errorf("vmess 链接不是 base64")
	}
	var v vmessShare
	if err := json.Unmarshal([]byte(d), &v); err != nil {
		return nil, fmt.Errorf("vmess 链接内容不是 JSON")
	}
	if rawTrue(v.Insecure) || rawTrue(v.SkipVerify) || rawTrue(v.InsecureShort) {
		return nil, errInsecure
	}
	port, err := parsePort(strings.Trim(string(v.Port), `"`))
	if err != nil {
		return nil, err
	}
	if aid := strings.Trim(string(v.Aid), `" `); aid != "" && aid != "0" {
		return nil, fmt.Errorf("VMess alterId>0（旧版 MD5 认证）已不受支持")
	}
	if v.Net == "tcp" && v.Type != "" && v.Type != "none" {
		return nil, fmt.Errorf("VMess 的 TCP 伪装头（%s）暂不支持", v.Type)
	}
	e := &Endpoint{Protocol: VMess, Host: v.Add, Port: port, Name: strings.TrimSpace(v.PS),
		Params: map[string]string{"method": strings.ToLower(v.Scy), "network": strings.ToLower(v.Net),
			"path": v.Path, "host_header": v.Host, "sni": v.SNI, "alpn": v.ALPN, "fp": strings.ToLower(v.FP)},
		Secret: map[string]string{"uuid": v.ID}}
	if e.Params["network"] == "tcp" || e.Params["network"] == "raw" {
		e.Params["network"] = ""
	}
	if e.Params["network"] == "grpc" {
		e.Params["service_name"], e.Params["path"] = v.Path, ""
	}
	if strings.EqualFold(v.TLS, "tls") {
		e.Params["security"] = "tls"
		if e.Params["sni"] == "" {
			e.Params["sni"] = firstNonEmpty(v.Host, sniDefault(v.Add))
		}
	}
	return e, nil
}

// jsonItem 是后台自有 JSON 导入格式（也兼容常见字段别名）。
type jsonItem struct {
	Protocol    string            `json:"protocol"`
	Type        string            `json:"type"`
	Host        string            `json:"host"`
	Server      string            `json:"server"`
	Address     string            `json:"address"`
	Port        json.RawMessage   `json:"port"`
	Username    string            `json:"username"`
	User        string            `json:"user"`
	Password    string            `json:"password"`
	Pass        string            `json:"pass"`
	UUID        string            `json:"uuid"`
	Name        string            `json:"name"`
	CountryCode string            `json:"country_code"`
	City        string            `json:"city"`
	Region      string            `json:"region"`
	SupportsUDP *bool             `json:"supports_udp"`
	Params      map[string]string `json:"params"`
	URI         string            `json:"uri"`
}

func parseJSON(text string, o ParseOptions) ([]Row, error) {
	var raw []json.RawMessage
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") {
		var wrap struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrap); err != nil || wrap.Items == nil {
			return nil, fmt.Errorf("JSON 需要是数组，或 {\"items\":[...]}")
		}
		raw = wrap.Items
	} else if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("JSON 格式错误")
	}
	if len(raw) > MaxRows {
		return nil, fmt.Errorf("一次最多导入 %d 行", MaxRows)
	}
	rows := make([]Row, 0, len(raw))
	for i, item := range raw {
		row := Row{Line: i + 1, Source: fmt.Sprintf("JSON 第 %d 项", i+1)}
		var s string
		if json.Unmarshal(item, &s) == nil {
			e, err := parseOne(strings.TrimSpace(s), o)
			if err != nil {
				row.Error = err.Error()
			} else {
				row.Endpoint = e
			}
			rows = append(rows, row)
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.DisallowUnknownFields()
		var it jsonItem
		if err := dec.Decode(&it); err != nil {
			row.Error = "字段不认识或格式错误"
			rows = append(rows, row)
			continue
		}
		if it.URI != "" {
			e, err := parseOne(strings.TrimSpace(it.URI), o)
			if err != nil {
				row.Error = err.Error()
			} else {
				mergeMeta(e, it)
				row.Endpoint = e
			}
			rows = append(rows, row)
			continue
		}
		e, err := itemEndpoint(it, o)
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Endpoint = e
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func mergeMeta(e *Endpoint, it jsonItem) {
	if it.Name != "" {
		e.Name = it.Name
	}
	if it.CountryCode != "" {
		e.CountryCode = it.CountryCode
	}
	if it.City != "" {
		e.City = it.City
	}
	if it.Region != "" {
		e.Region = it.Region
	}
	if it.SupportsUDP != nil && e.Protocol == SOCKS5 {
		e.SupportsUDP = *it.SupportsUDP
	}
}

func protocolAlias(p string) string {
	switch strings.ToUpper(strings.TrimSpace(p)) {
	case "SOCKS", "SOCKS5", "SOCKS5H":
		return SOCKS5
	case "HTTP":
		return HTTP
	case "HTTPS":
		return HTTPS
	case "SS", "SHADOWSOCKS", "SS2022", "SHADOWSOCKS_2022":
		return Shadowsocks
	case "TROJAN":
		return Trojan
	case "VLESS", "VLESS_REALITY":
		return VLESS
	case "VMESS":
		return VMess
	case "HYSTERIA2", "HY2":
		return Hysteria2
	case "TUIC":
		return TUIC
	}
	return strings.ToUpper(strings.TrimSpace(p))
}

func itemEndpoint(it jsonItem, o ParseOptions) (*Endpoint, error) {
	proto := protocolAlias(firstNonEmpty(it.Protocol, it.Type, o.DefaultProtocol))
	port, err := parsePort(strings.Trim(string(it.Port), `"`))
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for k, v := range it.Params {
		if k == "allowInsecure" || k == "insecure" {
			if rawTrue(json.RawMessage(v)) {
				return nil, errInsecure
			}
			continue
		}
		params[k] = v
	}
	e := &Endpoint{Protocol: proto, Host: firstNonEmpty(it.Host, it.Server, it.Address), Port: port,
		Params: params, Secret: map[string]string{}}
	switch proto {
	case SOCKS5, HTTP, HTTPS:
		e.Secret["user"] = firstNonEmpty(it.Username, it.User)
		e.Secret["pass"] = firstNonEmpty(it.Password, it.Pass)
		if proto == HTTPS {
			e.Params["security"] = "tls"
			if e.Params["sni"] == "" {
				e.Params["sni"] = sniDefault(e.Host)
			}
		}
		if proto == SOCKS5 {
			e.SupportsUDP = o.DefaultUDP
		}
	case VLESS, VMess:
		e.Secret["uuid"] = firstNonEmpty(it.UUID, it.User, it.Username)
	default:
		e.Secret["password"] = firstNonEmpty(it.Password, it.Pass)
	}
	mergeMeta(e, it)
	return e, nil
}

var csvColumns = map[string]string{
	"protocol": "protocol", "type": "protocol", "host": "host", "server": "host", "ip": "host", "address": "host",
	"port": "port", "username": "user", "user": "user", "password": "pass", "pass": "pass", "uuid": "uuid",
	"name": "name", "country_code": "country_code", "country": "country_code", "city": "city", "region": "region",
	"supports_udp": "supports_udp", "udp": "supports_udp",
}

func parseCSV(text string, o ParseOptions) ([]Row, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("CSV 表头读取失败")
	}
	cols := make([]string, len(header))
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		if c, ok := csvColumns[h]; ok {
			cols[i] = c
		} else if paramKeys[h] {
			cols[i] = "param:" + h
		} else {
			return nil, fmt.Errorf("CSV 列 %q 不认识", h)
		}
	}
	var rows []Row
	line := 1
	for {
		rec, err := r.Read()
		line++
		if err == io.EOF {
			break
		}
		row := Row{Line: line, Source: fmt.Sprintf("CSV 第 %d 行", line)}
		if err != nil {
			row.Error = "CSV 格式错误"
			rows = append(rows, row)
			continue
		}
		if len(rows) >= MaxRows {
			return nil, fmt.Errorf("一次最多导入 %d 行", MaxRows)
		}
		it := jsonItem{Params: map[string]string{}}
		for i, v := range rec {
			if i >= len(cols) {
				break
			}
			v = strings.TrimSpace(v)
			switch c := cols[i]; c {
			case "protocol":
				it.Protocol = v
			case "host":
				it.Host = v
			case "port":
				it.Port = json.RawMessage(strconv.Quote(v))
			case "user":
				it.Username = v
			case "pass":
				it.Password = v
			case "uuid":
				it.UUID = v
			case "name":
				it.Name = v
			case "country_code":
				it.CountryCode = v
			case "city":
				it.City = v
			case "region":
				it.Region = v
			case "supports_udp":
				b := v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
				it.SupportsUDP = &b
			default:
				it.Params[strings.TrimPrefix(c, "param:")] = v
			}
		}
		e, err := itemEndpoint(it, o)
		if err != nil {
			row.Error = err.Error()
		} else {
			row.Endpoint = e
		}
		rows = append(rows, row)
	}
	return rows, nil
}
