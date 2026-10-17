// Package traffic 实现流量上报入账、每日汇总、周期重置与配额判断（方案 8.5–8.7）。
// 上报幂等：traffic_reports(node_id, report_id)；重复上报返回 duplicate=true 且不累加。
package traffic
