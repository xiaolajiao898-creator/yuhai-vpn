import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/network/request_signer.dart';

void main() {
  // 与 server/internal/auth/signature_test.go 的 TestCanonicalRequestVector 共用向量
  test('canonical request matches Go implementation', () {
    final body = Uint8List.fromList(utf8.encode('{"a":1}'));
    final got = RequestSigner.canonical(
      method: 'post',
      path: '/v1/orders',
      query: {
        'limit': ['20'],
        'cursor': ['a b'],
        'z': ['2', '1'],
      },
      timestamp: '1789110000',
      nonce: 'h7Qm0x2Zk9rT4pWbJ1eYvA',
      deviceId: '0b6f3c1e-8a52-4d7e-9f10-2c4b7d9e1a33',
      body: body,
    );
    final want = 'YH1-ECDSA-P256-SHA256\nPOST\n/v1/orders\ncursor=a+b&limit=20&z=1&z=2\n1789110000\n'
        'h7Qm0x2Zk9rT4pWbJ1eYvA\n0b6f3c1e-8a52-4d7e-9f10-2c4b7d9e1a33\n${sha256.convert(body)}';
    expect(got, want);
  });

  test('empty body hashes to sha256 of empty string', () {
    final got = RequestSigner.canonical(
        method: 'GET', path: '/v1/me', query: {}, timestamp: '1', nonce: 'n', deviceId: 'd', body: Uint8List(0));
    expect(got.split('\n').last, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
  });
}
