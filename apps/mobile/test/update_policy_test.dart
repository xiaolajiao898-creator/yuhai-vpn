import 'dart:io';

import 'package:crypto/crypto.dart' as crypto;
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/update/update_download.dart';
import 'package:yuhai_vpn/features/update/update_policy.dart';

void main() {
  test('本地构建号决定可选升级、强制升级或已是最新', () {
    final latest = AppReleaseInfo(
      version: '1.2.0',
      build: 120,
      url: 'https://yhvpn.io/download/builds/app.exe',
      sha256: 'ab' * 32,
      size: 10,
      releasedAt: DateTime.utc(2026, 11, 13),
      minSupportedBuild: 100,
      notes: '修复连接',
    );
    expect(decideUpdate(localBuild: 120, latest: latest), UpdateKind.current);
    expect(decideUpdate(localBuild: 119, latest: latest), UpdateKind.optional);
    expect(decideUpdate(localBuild: 99, latest: latest), UpdateKind.forced);
  });

  test('只接受 yhvpn.io 和 api.yhvpn.io', () {
    expect(releaseUrlAllowed(Uri.parse('https://yhvpn.io/download/builds/a.exe')), isTrue);
    expect(releaseUrlAllowed(Uri.parse('https://api.yhvpn.io/v1/app/download/windows')), isTrue);
    expect(releaseUrlAllowed(Uri.parse('http://yhvpn.io/a.exe')), isFalse);
    expect(releaseUrlAllowed(Uri.parse('https://evil.example/a.exe')), isFalse);
    expect(releaseFileName(Uri.parse('https://yhvpn.io/download/builds/YuhaiVPN-Windows-x64.exe')), 'YuhaiVPN-Windows-x64.exe');
    expect(releaseFileName(Uri.parse('https://yhvpn.io/download/builds/../../secret')), 'secret');
  });

  test('跳转后的地址也必须在允许的站点里', () {
    final from = Uri.parse('https://yhvpn.io/download/latest/windows');
    expect(
      redirectedUrl(from, 302, '/download/builds/a.exe'),
      Uri.parse('https://yhvpn.io/download/builds/a.exe'),
    );
    expect(
      redirectedUrl(from, 302, 'https://api.yhvpn.io/v1/app/download/windows'),
      Uri.parse('https://api.yhvpn.io/v1/app/download/windows'),
    );
    expect(redirectedUrl(from, 200, 'https://evil.example/a.exe'), isNull);
    expect(() => redirectedUrl(from, 302, 'https://evil.example/a.exe'), throwsA(isA<UpdateRejected>()));
    expect(() => redirectedUrl(from, 301, 'http://yhvpn.io/a.exe'), throwsA(isA<UpdateRejected>()));
    expect(() => redirectedUrl(from, 308, 'https://user:secret@yhvpn.io/a.exe'), throwsA(isA<UpdateRejected>()));
  });

  test('校验不一致就删除文件', () async {
    final dir = await Directory.systemTemp.createTemp('yuhai-update');
    addTearDown(() => dir.delete(recursive: true));
    final file = File('${dir.path}/bad.bin')..writeAsBytesSync([1, 2, 3, 4]);
    expect(() => acceptVerifiedFile(file, 'aa' * 32), throwsA(isA<UpdateRejected>()));
    expect(file.existsSync(), isFalse);
  });

  test('校验一致才留下文件', () async {
    final bytes = [9, 8, 7, 6];
    final dir = await Directory.systemTemp.createTemp('yuhai-update-ok');
    addTearDown(() => dir.delete(recursive: true));
    final file = File('${dir.path}/ok.bin')..writeAsBytesSync(bytes);
    acceptVerifiedFile(file, crypto.sha256.convert(bytes).toString());
    expect(file.existsSync(), isTrue);
  });
}
