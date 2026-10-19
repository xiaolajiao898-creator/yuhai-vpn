import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/connection/data/session_guard.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';

NodeItem line(String id, String country, String region,
        {bool saturated = false, bool recommended = false, bool available = true}) =>
    NodeItem(
      id: id,
      name: id,
      countryCode: country,
      city: '',
      region: region,
      available: available,
      isRecommended: recommended,
      protocols: const ['WIREGUARD'],
      probeHost: 'node.invalid',
      probePort: 443,
      saturated: saturated,
      capacityUsers: saturated ? 10 : 100,
      onlineCount: saturated ? 10 : 1,
    );

void main() {
  test('退避到 30 秒封顶，卡死探测认得出', () {
    expect(backoffDelay(0), const Duration(seconds: 2));
    expect(backoffDelay(1), const Duration(seconds: 4));
    expect(backoffDelay(3), const Duration(seconds: 16));
    expect(backoffDelay(8), const Duration(seconds: 30));
    expect(tunnelSampleBad(null), isTrue);
    expect(tunnelSampleBad(799), isFalse);
    expect(tunnelSampleBad(800), isTrue);
  });

  test('国家相同才算同一地区，空国家才退回地区名', () {
    expect(lineArea('sg', 'ASIA'), 'country:SG');
    expect(lineArea('', 'ASIA'), 'region:ASIA');
    expect(lineArea('', ''), isNull);
    expect(sameLineArea(line('a', 'SG', 'ASIA'), line('b', 'JP', 'ASIA')), isFalse);
    expect(sameLineArea(line('a', 'SG', 'ASIA'), line('b', 'SG', 'EUROPE')), isTrue);
  });

  test('保护开着且不自动重连时只黑洞，不恢复本地网络', () {
    final decision = decideRecovery(
      leakProtect: true,
      autoRecover: false,
      networkChanged: false,
      failuresOnLine: 1,
      current: line('1', 'SG', 'ASIA'),
      lines: [line('1', 'SG', 'ASIA')],
    );
    expect(decision.action, RecoveryAction.hold);
    expect(decision.protected, isTrue);
    expect(decision.notice, contains('已保护'));
  });

  test('前两次重试当前线路，之后换同国未满载线路', () {
    final lines = [
      line('sg-full', 'SG', 'ASIA', saturated: true, recommended: true),
      line('sg-ok', 'SG', 'ASIA'),
      line('jp', 'JP', 'ASIA', recommended: true),
    ];
    final first = decideRecovery(
      leakProtect: true,
      autoRecover: true,
      networkChanged: false,
      failuresOnLine: 1,
      current: lines.first,
      lines: lines,
    );
    expect(first.action, RecoveryAction.retrySame);
    expect(first.lineId, 'sg-full');
    expect(first.delay, const Duration(seconds: 2));
    final third = decideRecovery(
      leakProtect: true,
      autoRecover: true,
      networkChanged: false,
      failuresOnLine: 3,
      current: lines.first,
      lines: lines,
    );
    expect(third.action, RecoveryAction.switchLine);
    expect(third.lineId, 'sg-ok');
    expect(third.notice, contains('已切换线路'));
    expect(third.notice, contains('已保护'));
  });

  test('同一地区没有线路时不跨境，网络变化先重试当前线路', () {
    final only = line('jp', 'JP', 'ASIA');
    final stuck = decideRecovery(
      leakProtect: false,
      autoRecover: true,
      networkChanged: false,
      failuresOnLine: 4,
      current: only,
      lines: [only, line('sg', 'SG', 'ASIA')],
    );
    expect(stuck.action, RecoveryAction.retrySame);
    expect(stuck.lineId, 'jp');
    expect(stuck.protected, isFalse);
    expect(stuck.notice, contains('不会跨地区'));
    final changed = decideRecovery(
      leakProtect: true,
      autoRecover: true,
      networkChanged: true,
      failuresOnLine: 9,
      current: only,
      lines: [only, line('sg', 'SG', 'ASIA')],
    );
    expect(changed.action, RecoveryAction.retrySame);
    expect(changed.delay, const Duration(seconds: 2));
    expect(changed.notice, contains('网络已切换'));
  });

  test('保护没装上时不说已保护', () {
    expect(
        protectionNotice('重连中 · 已保护', wanted: true, held: true), '重连中 · 已保护');
    expect(protectionNotice('重连中 · 已保护', wanted: true, held: false),
        '重连中 · 断网保护没有打开');
    expect(protectionNotice('重连中', wanted: false, held: false), '重连中');
  });
}
