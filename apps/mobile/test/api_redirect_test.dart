import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'session_lifecycle_test.dart' show clientFor, TestDeviceKey;

void main() {
  test('307 cannot forward a signed recovery body to another endpoint',
      () async {
    final target = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    final origin = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    var targetCalls = 0;
    target.listen((request) async {
      targetCalls++;
      request.response.statusCode = 200;
      request.response.write('{"data":{}}');
      await request.response.close();
    });
    origin.listen((request) async {
      await request.drain<void>();
      request.response.statusCode = 307;
      request.response.headers.set(
          HttpHeaders.locationHeader, 'http://127.0.0.1:${target.port}/stolen');
      await request.response.close();
    });
    final client = clientFor(SessionStore(), TestDeviceKey());
    client.dio.options.baseUrl = 'http://127.0.0.1:${origin.port}';
    try {
      await expectLater(
          client.selfSigned('/v1/account/recover',
              {'device_uid': 'test-device', 'secret': 'redirect-canary'}),
          throwsA(anything));
      expect(targetCalls, 0);
    } finally {
      client.dio.close(force: true);
      await origin.close(force: true);
      await target.close(force: true);
    }
  });
}
