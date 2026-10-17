package nodewire

import (
	"errors"
	"regexp"
	"strconv"
	"time"
)

// 节点流量上报的线格式（方案 8.5）。与心跳一样走严格 JSON：重复键、未知字段、
// 缺字段、null 一律拒绝。字节数在接口层是字符串，避免 JSON 数字精度问题；
// 上限取 IEEE-754 安全整数，与 App 侧的 traffic 查询保持同一口径。
type TrafficItem struct {
	CredentialID string `json:"credential_id"`
	UpBytes      string `json:"up_bytes"`
	DownBytes    string `json:"down_bytes"`
}

type TrafficReport struct {
	ReportID    string        `json:"report_id"`
	WindowStart string        `json:"window_start"`
	WindowEnd   string        `json:"window_end"`
	Items       []TrafficItem `json:"items"`
}

type TrafficReceipt struct {
	Accepted   bool   `json:"accepted"`
	Duplicate  bool   `json:"duplicate"`
	ItemCount  int    `json:"item_count"`
	ServerTime string `json:"server_time"`
}

// MaxTrafficBytes 是单个字段与任何累加结果的上限（2^53-1）。
const MaxTrafficBytes int64 = 1<<53 - 1

// MaxTrafficItems 限制单次上报的条目数，防止一次请求放大成超长事务。
const MaxTrafficItems = 1000

// TrafficWindow 是允许的最大统计窗口。方案规定每 60 秒上报一次；放宽到 10 分钟
// 以容忍节点重启后的补报，但不接受任意长的窗口。
const TrafficWindow = 10 * time.Minute

// TrafficLateness 是窗口结束时间允许落后当前时间的上限。超过说明节点时钟或积压异常，
// 这类数据不应当再影响实时权益扣减。
const TrafficLateness = time.Hour

var trafficUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ValidTrafficReportID 只接受小写规范形式的 UUIDv4：report_id 是幂等键，
// 大小写或格式变体会绕过唯一约束，导致同一份数据被重复计费。
func ValidTrafficReportID(s string) bool { return trafficUUID.MatchString(s) }

type ParsedTraffic struct {
	ReportID    string
	Start, End  time.Time
	Items       []ParsedTrafficItem
	TotalUp     int64
	TotalDown   int64
	Credentials []int64
}

type ParsedTrafficItem struct {
	CredentialID int64
	Up, Down     int64
}

func badTraffic() error { return errors.New("流量上报字段或格式无效") }

func trafficBytes(s string) (int64, error) {
	if !numberPattern.MatchString(s) {
		return 0, badTraffic()
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > MaxTrafficBytes {
		return 0, badTraffic()
	}
	return n, nil
}

// ParseTrafficReport 校验并规范化一次上报。now 用于判定窗口时效。
// 同一凭据在一次上报中只允许出现一次——重复出现无法区分「节点拆分了统计」与
// 「同一份数据被算了两遍」，直接拒绝比猜测安全。
func ParseTrafficReport(raw []byte, now time.Time) (ParsedTraffic, error) {
	var out ParsedTraffic
	var r TrafficReport
	if len(raw) > 262144 || StrictJSON(raw, &r) != nil {
		return out, badTraffic()
	}
	if !ValidTrafficReportID(r.ReportID) {
		return out, badTraffic()
	}
	if !timestampPattern.MatchString(r.WindowStart) || !timestampPattern.MatchString(r.WindowEnd) {
		return out, badTraffic()
	}
	start, err := time.Parse(time.RFC3339Nano, r.WindowStart)
	if err != nil {
		return out, badTraffic()
	}
	end, err := time.Parse(time.RFC3339Nano, r.WindowEnd)
	if err != nil {
		return out, badTraffic()
	}
	if !end.After(start) || end.Sub(start) > TrafficWindow {
		return out, badTraffic()
	}
	// 允许少量时钟超前，但不接受来自未来的窗口，也不接受过于陈旧的补报。
	if end.After(now.Add(time.Minute)) || now.Sub(end) > TrafficLateness {
		return out, badTraffic()
	}
	if r.Items == nil || len(r.Items) > MaxTrafficItems {
		return out, badTraffic()
	}
	seen := make(map[int64]bool, len(r.Items))
	out = ParsedTraffic{ReportID: r.ReportID, Start: start.UTC(), End: end.UTC(), Items: make([]ParsedTrafficItem, 0, len(r.Items))}
	for _, item := range r.Items {
		id, err := strconv.ParseInt(item.CredentialID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != item.CredentialID || seen[id] {
			return ParsedTraffic{}, badTraffic()
		}
		seen[id] = true
		up, err := trafficBytes(item.UpBytes)
		if err != nil {
			return ParsedTraffic{}, err
		}
		down, err := trafficBytes(item.DownBytes)
		if err != nil {
			return ParsedTraffic{}, err
		}
		if out.TotalUp > MaxTrafficBytes-up || out.TotalDown > MaxTrafficBytes-down {
			return ParsedTraffic{}, badTraffic()
		}
		out.TotalUp += up
		out.TotalDown += down
		out.Items = append(out.Items, ParsedTrafficItem{CredentialID: id, Up: up, Down: down})
		out.Credentials = append(out.Credentials, id)
	}
	return out, nil
}
