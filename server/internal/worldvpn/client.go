package worldvpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 请求格式：官网示例用 curl -F（multipart），PHP 示例把数组交给 CURLOPT_POSTFIELDS（也是 multipart）。
// 2026-11-07 用空 body、multipart、application/x-www-form-urlencoded、JSON 分别调用
// listServers / addVPN / getCredit（非真实批发账号）时，HTTP 200 且正文都是
// {"result":"error","message":"some options not posted"}。
// 因此这句报错不能证明「字段没送到」：不存在的批发账号和空 body 是同一句。
//
// 仍然采用 application/x-www-form-urlencoded，而不是 multipart。若对方用
// parse_str(file_get_contents('php://input')) 读参数，multipart 的原始 body 在 PHP
// 里经常是空的，就会对真实账号也返回 some options not posted；urlencoded 能被这种读法看到。
// 单元测试锁定的是文档里的成功 JSON，以及缺字段 / result=error。没有把真实批发密码放进测试。

const maxBody = 2 << 20

// APIError 是批发接口返回的业务失败。Message 已去掉我们自己发出的口令。
type APIError struct {
	Action  string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return "worldvpn: " + e.Action
	}
	return "worldvpn: " + e.Action + ": " + e.Message
}

// Client 是 WorldVPN 批发 API。零值不能用，用 NewClient。
type Client struct {
	BaseURL    string
	Username   string
	Password   string
	Sandbox    bool
	HTTP       *http.Client
	redactWith []string
}

func NewClient(s Settings) *Client {
	return &Client{
		BaseURL: s.BaseURL, Username: s.Username, Password: s.Password, Sandbox: s.Sandbox,
		HTTP:       &http.Client{Timeout: 25 * time.Second, CheckRedirect: refuseRedirect},
		redactWith: []string{s.Password, s.Username},
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("worldvpn: 拒绝重定向")
}

type vpnAccount struct {
	Username string
	Password string
}

type serverLine struct {
	ID   int64
	Name string
	IP   string
}

type peerCreated struct {
	WireGuardID string
	ClientIP    string
	DNS         []string
	ServerName  string
	PublicKey   string
}

func (c *Client) AddVPN(ctx context.Context, username, password string, months int) (vpnAccount, error) {
	body, err := c.post(ctx, "addVPN", map[string]string{
		"vpnUsername": username, "vpnPassword": password, "period": itoa(int64(months)),
	}, c.Sandbox, password)
	if err != nil {
		return vpnAccount{}, err
	}
	user, err := requiredString(body, "vpnUsername")
	if err != nil {
		return vpnAccount{}, err
	}
	pass, err := requiredString(body, "vpnPassword")
	if err != nil {
		return vpnAccount{}, err
	}
	return vpnAccount{Username: user, Password: pass}, nil
}

func (c *Client) RenewVPN(ctx context.Context, username string, months int) error {
	_, err := c.post(ctx, "renewVPN", map[string]string{
		"vpnUsername": username, "period": itoa(int64(months)),
	}, c.Sandbox)
	return err
}

func (c *Client) DeleteVPN(ctx context.Context, username string, sandbox bool) error {
	_, err := c.post(ctx, "deleteVPN", map[string]string{"vpnUsername": username}, sandbox)
	return err
}

func (c *Client) ChangePassword(ctx context.Context, username, password string) error {
	_, err := c.post(ctx, "changePassword", map[string]string{
		"vpnUsername": username, "vpnPassword": password,
	}, c.Sandbox, password)
	return err
}

func (c *Client) GetTrial(ctx context.Context, email string) (vpnAccount, error) {
	body, err := c.post(ctx, "getTrial", map[string]string{"email": email}, c.Sandbox, email)
	if err != nil {
		return vpnAccount{}, err
	}
	user, err := requiredString(body, "vpnUsername")
	if err != nil {
		return vpnAccount{}, err
	}
	pass, err := requiredString(body, "vpnPassword")
	if err != nil {
		return vpnAccount{}, err
	}
	return vpnAccount{Username: user, Password: pass}, nil
}

func (c *Client) ListServers(ctx context.Context) ([]serverLine, error) {
	body, err := c.post(ctx, "listServers", nil, c.Sandbox)
	if err != nil {
		return nil, err
	}
	return parseServers(body["servers"])
}

func (c *Client) GetCredit(ctx context.Context) (float64, error) {
	body, err := c.post(ctx, "getCredit", nil, c.Sandbox)
	if err != nil {
		return 0, err
	}
	credit, ok := body["credit"].(float64)
	if !ok {
		return 0, &APIError{Action: "getCredit", Message: "缺少 credit"}
	}
	return credit, nil
}

func (c *Client) GetAccounts(ctx context.Context) ([][3]string, error) {
	body, err := c.post(ctx, "getAccounts", nil, c.Sandbox)
	if err != nil {
		return nil, err
	}
	raw, ok := body["accounts"].([]any)
	if !ok {
		return nil, &APIError{Action: "getAccounts", Message: "缺少 accounts"}
	}
	out := make([][3]string, 0, len(raw))
	for _, item := range raw {
		row, ok := item.([]any)
		if !ok || len(row) < 3 {
			return nil, &APIError{Action: "getAccounts", Message: "账号行格式不对"}
		}
		id, ok := asID(row[0])
		if !ok {
			return nil, &APIError{Action: "getAccounts", Message: "账号编号不对"}
		}
		name, ok := row[1].(string)
		exp, ok2 := row[2].(string)
		if !ok || !ok2 || name == "" || exp == "" {
			return nil, &APIError{Action: "getAccounts", Message: "账号行缺字段"}
		}
		out = append(out, [3]string{itoa(id), name, exp})
	}
	return out, nil
}

func (c *Client) CheckAuth(ctx context.Context, username, password string) error {
	_, err := c.post(ctx, "checkAuth", map[string]string{
		"vpnUsername": username, "vpnPassword": password,
	}, c.Sandbox, password)
	return err
}

func (c *Client) AddWireGuardPeer(ctx context.Context, username string, serverID int64) (peerCreated, error) {
	body, err := c.post(ctx, "addWireGuardPeer", map[string]string{
		"vpnUsername": username, "serverId": itoa(serverID),
	}, c.Sandbox)
	if err != nil {
		return peerCreated{}, err
	}
	id, err := requiredString(body, "wireguardId")
	if err != nil {
		if n, ok := asID(body["wireguardId"]); ok {
			id = itoa(n)
		} else {
			return peerCreated{}, err
		}
	}
	ip, err := requiredString(body, "clientIp")
	if err != nil {
		return peerCreated{}, err
	}
	server, _ := body["server"].(map[string]any)
	name, _ := server["name"].(string)
	pub, _ := server["publicKey"].(string)
	dns := splitDNS(stringField(body, "dns"))
	return peerCreated{WireGuardID: id, ClientIP: ip, DNS: dns, ServerName: name, PublicKey: pub}, nil
}

func (c *Client) GetWireGuardConfig(ctx context.Context, username, wireguardID string) (wireGuardMaterial, error) {
	fields := map[string]string{"vpnUsername": username}
	if wireguardID != "" {
		fields["wireguardId"] = wireguardID
	}
	body, err := c.post(ctx, "getWireGuardConfig", fields, c.Sandbox)
	if err != nil {
		return wireGuardMaterial{}, err
	}
	text, err := requiredString(body, "config")
	if err != nil {
		return wireGuardMaterial{}, err
	}
	m, err := parseWireGuardConfig(text)
	if err != nil {
		return wireGuardMaterial{}, &APIError{Action: "getWireGuardConfig", Message: "配置内容不完整"}
	}
	return m, nil
}

func (c *Client) DeleteWireGuardPeer(ctx context.Context, username, wireguardID string, sandbox bool) error {
	_, err := c.post(ctx, "deleteWireGuardPeer", map[string]string{
		"vpnUsername": username, "wireguardId": wireguardID,
	}, sandbox)
	return err
}

func (c *Client) post(ctx context.Context, action string, fields map[string]string, sandbox bool, extraSecrets ...string) (map[string]any, error) {
	if c.HTTP == nil || c.BaseURL == "" || c.Username == "" || c.Password == "" {
		return nil, errors.New("worldvpn: 客户端未配置")
	}
	form := url.Values{}
	form.Set("resellerUsername", c.Username)
	form.Set("resellerPassword", c.Password)
	for k, v := range fields {
		form.Set(k, v)
	}
	if sandbox {
		form.Set("mode", "test")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/"+action, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yuhai-vpn/worldvpn")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worldvpn: %s 请求失败", action)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("worldvpn: %s 读取失败", action)
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("worldvpn: %s 响应过大", action)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Action: action, Message: "HTTP " + resp.Status}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, &APIError{Action: action, Message: "响应不是 JSON 对象"}
	}
	if failed(body) {
		return nil, &APIError{Action: actionOr(body, action), Message: c.redact(stringField(body, "message"), extraSecrets...)}
	}
	if !succeeded(body) {
		return nil, &APIError{Action: actionOr(body, action), Message: "缺少成功标记"}
	}
	return body, nil
}

func (c *Client) redact(msg string, extra ...string) string {
	msg = strings.TrimSpace(msg)
	for _, secret := range append(append([]string{}, c.redactWith...), extra...) {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func failed(body map[string]any) bool {
	return strings.EqualFold(stringField(body, "result"), "error") || strings.EqualFold(stringField(body, "status"), "error")
}

func succeeded(body map[string]any) bool {
	return strings.EqualFold(stringField(body, "result"), "success") || strings.EqualFold(stringField(body, "status"), "success")
}

func actionOr(body map[string]any, fallback string) string {
	if a := stringField(body, "action"); a != "" {
		return a
	}
	return fallback
}

func stringField(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

func requiredString(body map[string]any, key string) (string, error) {
	s := strings.TrimSpace(stringField(body, key))
	if s == "" {
		if n, ok := asID(body[key]); ok {
			return itoa(n), nil
		}
		return "", &APIError{Action: actionOr(body, key), Message: "缺少 " + key}
	}
	return s, nil
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
