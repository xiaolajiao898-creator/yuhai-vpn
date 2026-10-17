import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/providers.dart';

void main() {
  test('release rejects development addresses and ambiguous API URLs', () {
    expect(isSecureApiBaseUrl('https://vpn.example.com', debug: false), isTrue);
    for (final value in [
      'http://vpn.example.com',
      'https://',
      'https://127.0.0.1',
      'https://10.0.2.2',
      'https://localhost',
      'https://vpn.local',
      'https://vpn.test',
      'https://[::1]',
      'https://name:password@vpn.example.com',
      'https://vpn.example.com?q=1',
      'https://vpn.example.com#fragment',
      'https://vpn.example.com/api',
      'https://vpn.example.com:65536',
      ' https://vpn.example.com'
    ]) {
      expect(isSecureApiBaseUrl(value, debug: false), isFalse, reason: value);
    }
  });
  test('debug cleartext is limited to exact development loopback hosts', () {
    for (final value in [
      'http://10.0.2.2:8080',
      'http://10.0.2.2:18080',
      'http://127.0.0.1:18080',
      'http://localhost:18080'
    ]) {
      expect(isSecureApiBaseUrl(value, debug: true), isTrue, reason: value);
    }
    for (final value in [
      'http://vpn.example.com',
      'http://10.0.2.3:18080',
      'http://localhost.evil.com',
      'http://10.0.2.2:18080@evil.com',
      'http://10.0.2.2:18080?password=bad'
    ]) {
      expect(isSecureApiBaseUrl(value, debug: true), isFalse, reason: value);
    }
  });
}
