package nodewire

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 流量上报直接驱动权益扣减，所以解析层的每一处拒绝都关系到会不会多扣或少扣。

func trafficNow() time.Time { return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC) }

func trafficBody(reportID, start, end string, items string) []byte {
	return []byte(fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":[%s]}`, reportID, start, end, items))
}

const okReportID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

func stamp2(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func TestParseTrafficReportAccepts(t *testing.T) {
	now := trafficNow()
	raw := trafficBody(okReportID, stamp2(now.Add(-time.Minute)), stamp2(now),
		`{"credential_id":"7","up_bytes":"100","down_bytes":"200"},{"credential_id":"9007199254740993","up_bytes":"0","down_bytes":"0"}`)
	out, err := ParseTrafficReport(raw, now)
	if err != nil {
		t.Fatalf("合法上报被拒绝：%v", err)
	}
	if len(out.Items) != 2 || out.TotalUp != 100 || out.TotalDown != 200 {
		t.Fatalf("解析结果不符：%+v", out)
	}
	if out.Items[0].CredentialID != 7 || out.Items[1].CredentialID != 9007199254740993 {
		t.Fatalf("凭据编号不符：%+v", out.Items)
	}
	// 空条目列表是合法的：节点在窗口内没有流量也应当照常上报，用于确认存活。
	empty := trafficBody(okReportID, stamp2(now.Add(-time.Minute)), stamp2(now), "")
	if out, err := ParseTrafficReport(empty, now); err != nil || len(out.Items) != 0 {
		t.Fatalf("空上报应当被接受：%v", err)
	}
}

func TestParseTrafficReportRejects(t *testing.T) {
	now := trafficNow()
	minuteAgo, nowStamp := stamp2(now.Add(-time.Minute)), stamp2(now)
	item := `{"credential_id":"7","up_bytes":"1","down_bytes":"1"}`
	big := fmt.Sprint(MaxTrafficBytes + 1)

	cases := []struct {
		name string
		raw  []byte
	}{
		{"report_id 非 UUIDv4", trafficBody("not-a-uuid", minuteAgo, nowStamp, item)},
		{"report_id 为 v1", trafficBody("3f2504e0-4f89-11d3-9a0c-0305e82c3301", minuteAgo, nowStamp, item)},
		{"report_id 大写", trafficBody(strings.ToUpper(okReportID), minuteAgo, nowStamp, item)},
		{"窗口倒置", trafficBody(okReportID, nowStamp, minuteAgo, item)},
		{"窗口起止相同", trafficBody(okReportID, nowStamp, nowStamp, item)},
		{"窗口过长", trafficBody(okReportID, stamp2(now.Add(-11*time.Minute)), nowStamp, item)},
		{"窗口来自未来", trafficBody(okReportID, stamp2(now.Add(time.Hour)), stamp2(now.Add(2*time.Hour)), item)},
		{"补报过旧", trafficBody(okReportID, stamp2(now.Add(-3*time.Hour)), stamp2(now.Add(-2*time.Hour)), item)},
		{"时间带时区偏移", trafficBody(okReportID, "2026-09-13T20:00:00+08:00", nowStamp, item)},
		{"凭据编号为零", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"0","up_bytes":"1","down_bytes":"1"}`)},
		{"凭据编号前导零", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"07","up_bytes":"1","down_bytes":"1"}`)},
		{"凭据编号为负", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"-1","up_bytes":"1","down_bytes":"1"}`)},
		{"同一凭据重复出现", trafficBody(okReportID, minuteAgo, nowStamp, item+","+item)},
		{"字节为负", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"7","up_bytes":"-1","down_bytes":"1"}`)},
		{"字节前导零", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"7","up_bytes":"01","down_bytes":"1"}`)},
		{"字节为小数", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"7","up_bytes":"1.5","down_bytes":"1"}`)},
		{"字节超出安全整数", trafficBody(okReportID, minuteAgo, nowStamp, fmt.Sprintf(`{"credential_id":"7","up_bytes":%q,"down_bytes":"1"}`, big))},
		{"字节为 JSON 数字而非字符串", []byte(fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":[{"credential_id":"7","up_bytes":1,"down_bytes":1}]}`, okReportID, minuteAgo, nowStamp))},
		{"items 为 null", []byte(fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":null}`, okReportID, minuteAgo, nowStamp))},
		{"缺少字段", []byte(fmt.Sprintf(`{"report_id":%q,"window_start":%q,"items":[]}`, okReportID, minuteAgo))},
		{"多余字段", []byte(fmt.Sprintf(`{"report_id":%q,"window_start":%q,"window_end":%q,"items":[],"extra":1}`, okReportID, minuteAgo, nowStamp))},
		{"重复键", []byte(fmt.Sprintf(`{"report_id":%q,"report_id":%q,"window_start":%q,"window_end":%q,"items":[]}`, okReportID, okReportID, minuteAgo, nowStamp))},
		{"条目内多余字段", trafficBody(okReportID, minuteAgo, nowStamp, `{"credential_id":"7","up_bytes":"1","down_bytes":"1","peer":"x"}`)},
		{"非 JSON", []byte("nope")},
		{"空输入", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseTrafficReport(c.raw, now); err == nil {
				t.Fatal("应当拒绝但被接受")
			}
		})
	}
}

func TestParseTrafficReportRejectsOversizeAndTooManyItems(t *testing.T) {
	now := trafficNow()
	minuteAgo, nowStamp := stamp2(now.Add(-time.Minute)), stamp2(now)
	var items []string
	for i := 1; i <= MaxTrafficItems+1; i++ {
		items = append(items, fmt.Sprintf(`{"credential_id":"%d","up_bytes":"1","down_bytes":"1"}`, i))
	}
	if _, err := ParseTrafficReport(trafficBody(okReportID, minuteAgo, nowStamp, strings.Join(items, ",")), now); err == nil {
		t.Fatal("超过条目上限应当被拒绝")
	}
	oversize := append(trafficBody(okReportID, minuteAgo, nowStamp, ""), make([]byte, 262145)...)
	if _, err := ParseTrafficReport(oversize, now); err == nil {
		t.Fatal("超大请求体应当被拒绝")
	}
}

// 累加溢出必须在解析阶段挡住，绝不能带着已溢出的总量进入扣减事务。
func TestParseTrafficReportRejectsAccumulatedOverflow(t *testing.T) {
	now := trafficNow()
	half := fmt.Sprint(MaxTrafficBytes/2 + 1)
	items := fmt.Sprintf(`{"credential_id":"1","up_bytes":%q,"down_bytes":"0"},{"credential_id":"2","up_bytes":%q,"down_bytes":"0"}`, half, half)
	if _, err := ParseTrafficReport(trafficBody(okReportID, stamp2(now.Add(-time.Minute)), stamp2(now), items), now); err == nil {
		t.Fatal("上行累加溢出应当被拒绝")
	}
}

func TestValidTrafficReportID(t *testing.T) {
	if !ValidTrafficReportID(okReportID) {
		t.Fatal("合法 UUIDv4 被拒绝")
	}
	for _, bad := range []string{"", "3f2504e0-4f89-41d3-9a0c-0305e82c330", strings.ToUpper(okReportID),
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301", "3f2504e04f8941d39a0c0305e82c3301"} {
		if ValidTrafficReportID(bad) {
			t.Fatalf("非法 report_id 被接受：%q", bad)
		}
	}
}
