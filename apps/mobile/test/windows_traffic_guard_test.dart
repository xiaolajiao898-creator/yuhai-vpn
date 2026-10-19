import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/windows_traffic_guard.dart';

void main() {
  test('放行规则比拦截规则重，空名单不能假装已保护', () {
    expect(TrafficGuardPlan.permitWeight, greaterThan(TrafficGuardPlan.blockWeight));
    const plan = TrafficGuardPlan(permitPaths: ['C:\\app.exe', 'C:\\xray.exe'], blockAllOthers: true);
    expect(plan.permitPaths, hasLength(2));
    expect(plan.blockAllOthers, isTrue);
  });
}
