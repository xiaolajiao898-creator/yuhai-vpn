import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';

void main() {
  test('node unavailable reasons remain distinct', () {
    for (final entry in {
      'TIER': '需要更高等级套餐', 'MAINTENANCE': '维护中',
      'OFFLINE': '节点暂时离线', 'NOT_READY': '节点准备中',
      'UNKNOWN': '节点暂不可用',
    }.entries) {
      final item = NodeItem.fromJson({
        'id': '9007199254740993', 'name': '香港', 'country_code': 'HK',
        'region': 'ASIA', 'available': false, 'locked_reason': entry.key,
        'is_recommended': false, 'protocols': <String>[],
      });
      expect(item.unavailableLabel, entry.value);
      expect(item.available, isFalse);
      expect(item.id, '9007199254740993');
    }
  });
}
