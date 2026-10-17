import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';

class _MemKeys implements WireGuardKeyStore {
  final map = <String, String>{};
  @override
  Future<String?> read(String key) async => map[key];
  @override
  Future<void> write(String key, String value) async => map[key] = value;
}

void main() {
  test('wg dump 解析：取第一个 peer 的握手时间与收发字节', () {
    const dump = 'PRIVKEY\tPUBKEY\t51820\toff\n'
        'SERVERPUB\t(none)\t1.2.3.4:51820\t0.0.0.0/0,::/0\t1790000000\t2048\t1024\t25\n';
    final d = WindowsWireGuardEngine.parseDump(dump);
    expect(d, isNotNull);
    expect(d!.handshake, 1790000000);
    expect(d.rx, 2048);
    expect(d.tx, 1024);
    expect(WindowsWireGuardEngine.parseDump('only-interface-line\n'), isNull);
    expect(WindowsWireGuardEngine.parseDump('a\tb\tc\td\nx\ty\tz\tw\tbad\t1\t2\t3\n'), isNull);
  });

  test('同一身份的公钥稳定，不同身份互不相同，私钥只进密钥存储', () async {
    final keys = _MemKeys();
    final e = WindowsWireGuardEngine(
        keys: keys,
        run: (exe, args) async => ProcessResult(0, 0, '', ''));
    final a1 = await e.publicKey('100:200');
    final a2 = await e.publicKey('100:200');
    final b = await e.publicKey('100:201');
    expect(a1, a2);
    expect(a1, isNot(b));
    expect(a1.length, 44);
    expect(keys.map.keys, containsAll(['wg-private:100:200', 'wg-private:100:201']));
    expect(keys.map.values, isNot(contains(a1)));
    e.dispose();
  });
}
