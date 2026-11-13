import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/tunnel_diagnostic.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';
import 'package:yuhai_vpn/platform/vpn/wireguard_profile.dart';

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

  test('安装失败会写明退出码，配置用 CRLF 且不含打不开的 IPv6', () async {
    final dir = await Directory.systemTemp.createTemp('yuhai-wg');
    addTearDown(() => dir.delete(recursive: true));
    final issued = base64Encode(List<int>.filled(32, 3));
    final server = base64Encode(List<int>.filled(32, 4));
    final now = DateTime.utc(2026, 11, 13, 12);
    final profile = WireGuardProfile.parseIssued({
      'line_id': 'w43',
      'server_id': '43',
      'expires_at': now.add(const Duration(minutes: 5)).toIso8601String(),
      'endpoint': 'vpn.example.com:51820',
      'server_public_key': server,
      'address': '10.0.0.2/32',
      'dns': ['1.1.1.1', '2606:4700:4700::1111'],
      'mtu': 1420,
      'interface_private_key': issued,
    }, lineId: 'w43', identity: '8302571644:28', now: now);
    String? written;
    final engine = WindowsWireGuardEngine(
      keys: _MemKeys(),
      programDataDir: dir.path,
      now: () => now,
      ipv6Enabled: () async => false,
      resolveHost: (host) async {
        expect(host, 'vpn.example.com');
        return [InternetAddress('203.0.113.10')];
      },
      run: (exe, args) async {
        final base = exe.split(r'\').last.toLowerCase();
        if (base == 'powershell.exe') {
          return ProcessResult(0, 0, 'S-1-5-32-544\r\nS-1-5-18\r\nS-1-5-32-544\r\n', '');
        }
        if (args.contains('/installtunnelservice')) {
          written = File(args.last).readAsStringSync();
          return ProcessResult(0, 1, '', 'Access is denied.');
        }
        return ProcessResult(0, args.contains('query') ? 1 : 0, '', '');
      },
    );
    final pending = engine.failures.first;
    await engine.connect(profile);
    final failure = await pending.timeout(const Duration(seconds: 2));
    expect(failure.code, 'CONNECT_FAILED');
    expect(failure.message, contains('退出码 1'));
    expect(failure.message, contains('管理员'));
    expect(failure.message, isNot(contains(issued)));
    expect(written, isNotNull);
    expect(written, contains('\r\n'));
    expect(written, contains('MTU = 1280'));
    expect(written, contains('Endpoint = 203.0.113.10:51820'));
    expect(written, contains('DNS = 1.1.1.1'));
    expect(written, isNot(contains('2606:')));
    expect(written, contains('AllowedIPs = 0.0.0.0/0'));
    expect(written, isNot(contains('::/0')));
    expect(engine.tunnelDiagnostic!.stage, 'install');
    expect(engine.tunnelDiagnostic!.logSummary, isNot(contains(issued)));
    expect(redactTunnelText('PrivateKey = $issued'), isNot(contains(issued)));
    engine.dispose();
  });
}
