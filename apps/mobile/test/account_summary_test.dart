import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/home/presentation/account_summary.dart';

void main() {
  test('到期时间按本地时区显示，空套餐名回落到等级', () {
    final line = accountSummaryLine({
      'plan_name': '',
      'tier': 1,
      'device_count': 2,
      'device_limit': 3,
      'period_quota_bytes': null,
      'period_used_bytes': 0,
      'expire_at': '2026-11-17T19:56:00Z',
    }, toLocal: (utc) => utc.add(const Duration(hours: 8)));
    expect(line, 'L1 套餐 · 设备 2/3 · 流量 不限 · 到期 2026-11-18 03:56');
  });

  test('没有套餐和流量用尽用不同文案', () {
    expect(accountSummaryLine({'blocked_reason': 'NO_PLAN'}), '还没有套餐 · 去购买或兑换');
    expect(
      trafficLabel({'period_quota_bytes': 1024, 'period_used_bytes': 512}),
      '512 B / 1.0 KB',
    );
  });
}
