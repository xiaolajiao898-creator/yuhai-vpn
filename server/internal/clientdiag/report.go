package clientdiag

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuhai-vpn/server/internal/httpx"
)

var errBad = httpx.NewError(httpx.ErrBadRequest, "诊断内容无效")

// Report 是一条已经去掉私钥的连接失败诊断。
type Report struct {
	UserID       int64
	DeviceID     int64
	AppVersion   string
	Platform     string
	Stage        string
	ErrorCode    string
	ErrorText    string
	ServiceState string
	LogSummary   string
}

// Item 是后台列表里的一行。编号用十进制字符串，避免后台页面把大整数收成浮点。
type Item struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	DeviceID     string    `json:"device_id"`
	AppVersion   string    `json:"app_version"`
	Platform     string    `json:"platform"`
	Stage        string    `json:"stage"`
	ErrorCode    string    `json:"error_code"`
	ErrorText    string    `json:"error_text"`
	ServiceState string    `json:"service_state"`
	LogSummary   string    `json:"log_summary"`
	CreatedAt    time.Time `json:"created_at"`
}

var stages = map[string]bool{
	"prepare": true, "install": true, "service": true, "handshake": true, "connect": true,
}

var keyPattern = regexp.MustCompile(`[A-Za-z0-9+/]{43}=`)
var privateKeyLine = regexp.MustCompile(`(?i)(private\s*key\s*[=:]\s*)\S+`)
var errorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// Redact 去掉 WireGuard 私钥，并丢掉控制字符。日志只留尾部。
func Redact(raw string, limit int, tail bool) string {
	text := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' || r >= 0x20 {
			return r
		}
		return -1
	}, raw)
	text = keyPattern.ReplaceAllString(text, "[redacted]")
	text = privateKeyLine.ReplaceAllString(text, "${1}[redacted]")
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if limit > 0 && len(runes) > limit {
		if tail {
			text = string(runes[len(runes)-limit:])
		} else {
			text = string(runes[:limit])
		}
	}
	return text
}

// Normalize 校验客户端上报，并把密钥从每一段文本里去掉。
func Normalize(in Report) (Report, error) {
	in.Stage = strings.TrimSpace(in.Stage)
	if !stages[in.Stage] {
		return Report{}, errBad
	}
	in.ErrorCode = strings.TrimSpace(in.ErrorCode)
	if in.ErrorCode != "" && !errorCodePattern.MatchString(in.ErrorCode) {
		return Report{}, errBad
	}
	in.ErrorText = Redact(in.ErrorText, 500, false)
	in.ServiceState = Redact(in.ServiceState, 200, false)
	in.LogSummary = Redact(in.LogSummary, 2000, true)
	in.AppVersion = token(in.AppVersion, 32)
	in.Platform = token(in.Platform, 16)
	if in.UserID <= 0 || in.DeviceID <= 0 {
		return Report{}, errBad
	}
	return in, nil
}

func token(v string, n int) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		if r > 127 || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= n {
			break
		}
	}
	return b.String()
}

// PG 把诊断追加进 client_connect_diagnostics，并给后台分页列出。
type PG struct {
	Pool *pgxpool.Pool
}

func (p *PG) Insert(ctx context.Context, row Report) error {
	_, err := p.Pool.Exec(ctx, `
INSERT INTO client_connect_diagnostics
  (user_id, device_id, app_version, platform, stage, error_code, error_text, service_state, log_summary)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		row.UserID, row.DeviceID, row.AppVersion, row.Platform, row.Stage,
		row.ErrorCode, row.ErrorText, row.ServiceState, row.LogSummary)
	return err
}

func (p *PG) List(ctx context.Context, userID, deviceID int64, limit, offset int) ([]Item, int64, error) {
	where := "WHERE TRUE"
	args := make([]any, 0, 4)
	if userID > 0 {
		args = append(args, userID)
		where += " AND user_id = $1"
	}
	if deviceID > 0 {
		args = append(args, deviceID)
		if userID > 0 {
			where += " AND device_id = $2"
		} else {
			where += " AND device_id = $1"
		}
	}
	var total int64
	if err := p.Pool.QueryRow(ctx, "SELECT count(*) FROM client_connect_diagnostics "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limitPos := len(args) + 1
	offsetPos := len(args) + 2
	args = append(args, limit, offset)
	query := "SELECT id, user_id, device_id, app_version, platform, stage, error_code, error_text, service_state, log_summary, created_at FROM client_connect_diagnostics " +
		where + " ORDER BY created_at DESC, id DESC LIMIT $" + itoa(limitPos) + " OFFSET $" + itoa(offsetPos)
	rows, err := p.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var item Item
		var id, user, device int64
		if err := rows.Scan(&id, &user, &device, &item.AppVersion, &item.Platform, &item.Stage, &item.ErrorCode, &item.ErrorText, &item.ServiceState, &item.LogSummary, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		item.ID = itoa64(id)
		item.UserID = itoa64(user)
		item.DeviceID = itoa64(device)
		item.ErrorText = Redact(item.ErrorText, 500, false)
		item.LogSummary = Redact(item.LogSummary, 2000, true)
		item.ServiceState = Redact(item.ServiceState, 200, false)
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
