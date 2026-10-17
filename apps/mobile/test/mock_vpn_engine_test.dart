import 'dart:math';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

void main() {
  test('connect → connected → disconnect', () async {
    final engine = MockVpnEngine(random: Random(7));
    final seen = <VpnStatus>[];
    final sub = engine.status.listen(seen.add);
    await engine.connect(const ConnectProfile(nodeId: '1', protocol: 'MOCK', server: 'mock'));
    expect(engine.current, anyOf(VpnStatus.connected, VpnStatus.error));
    await engine.disconnect();
    expect(seen.first, VpnStatus.connecting);
    expect(engine.current, VpnStatus.disconnected);
    await sub.cancel();
    engine.dispose();
  });

  test('probe is stable per host', () async {
    final engine = MockVpnEngine();
    final a = await engine.probe('jp01.example.net', 443);
    final b = await engine.probe('jp01.example.net', 443);
    expect(a, b);
    engine.dispose();
  });
}
