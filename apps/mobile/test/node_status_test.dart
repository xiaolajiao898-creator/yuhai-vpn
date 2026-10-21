import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';

void main() {
  test('node unavailable reasons remain distinct', () {
    for (final entry in {
      'TIER': '需要更高等级套餐',
      'MAINTENANCE': '维护中',
      'OFFLINE': '节点暂时离线',
      'NOT_READY': '节点准备中',
      'UNKNOWN': '节点暂不可用',
    }.entries) {
      final item = NodeItem.fromJson({
        'id': '9007199254740993',
        'name': '香港',
        'country_code': 'HK',
        'region': 'ASIA',
        'available': false,
        'locked_reason': entry.key,
        'is_recommended': false,
        'protocols': <String>[],
      });
      expect(item.unavailableLabel, entry.value);
      expect(item.available, isFalse);
      expect(item.id, '9007199254740993');
    }
  });

  test('现网节点没有 00046 的在线数、容量和满载字段时仍可连接', () {
    final item = NodeItem.fromJson({
      'id': '12',
      'name': '香港',
      'country_code': 'HK',
      'city': '香港',
      'region': 'ASIA',
      'available': true,
      'locked_reason': null,
      'load': 20,
      'is_recommended': true,
      'probe': {'host': 'probe.example', 'port': 443},
      'protocols': ['WIREGUARD'],
    });
    expect(item.available, isTrue);
    expect(item.onlineCount, isNull);
    expect(item.capacityUsers, isNull);
    expect(item.saturated, isNull);
    expect(item.isSaturated, isFalse);
    expect(item.probeHost, 'probe.example');
    expect(item.protocols, ['WIREGUARD']);
  });
}
