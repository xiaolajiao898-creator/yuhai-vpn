import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/auto_select.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';

NodeItem line(String id,
        {bool available = true,
        bool recommended = false,
        String? host,
        List<String> protocols = const ['WIREGUARD']}) =>
    NodeItem(
      id: id,
      name: id,
      countryCode: 'HK',
      city: '香港',
      region: 'ASIA',
      available: available,
      isRecommended: recommended,
      probeHost: host,
      probePort: host == null ? null : 443,
      protocols: protocols,
    );

void main() {
  test('只有出口时自动选择仍然可用', () {
    final hk = line('e12', protocols: const ['VLESS']);
    expect(canAutoSelect(hk, egressSupported: false), isFalse);
    expect(canAutoSelect(hk, egressSupported: true), isTrue);
    expect(preferredEgress([hk])?.id, 'e12');
  });

  test('自动选择优先测速成功的节点，否则选推荐出口', () {
    final slow = line('1', host: 'slow.example');
    final fast = line('2', host: 'fast.example');
    expect(fastestProbed([slow, fast], {'1': 80, '2': 20})?.id, '2');
    expect(fastestProbed([slow], {'1': null}), isNull);
    final other = line('e2', protocols: const ['VLESS']);
    final recommended = line('e9', recommended: true, protocols: const ['VLESS']);
    expect(preferredEgress([other, recommended])?.id, 'e9');
  });
}
