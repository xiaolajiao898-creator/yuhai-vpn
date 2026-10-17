import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/wireguard_profile.dart';

final profileNow = DateTime.utc(2026, 9, 17, 12);
final clientPublic = base64Encode(List<int>.filled(32, 1));
final serverPublic = base64Encode(List<int>.filled(32, 2));

Map<String, dynamic> profileResponse({DateTime? now}) => {
      'expires_at':
          (now ?? profileNow).add(const Duration(minutes: 5)).toIso8601String(),
      'profiles': [
        {
          'node_id': '1',
          'outbounds': [
            {
              'protocol': 'WIREGUARD',
              'endpoint': 'vpn.example.com:51820',
              'server_public_key': serverPublic,
              'address': '10.20.0.16/32',
              'dns': ['1.1.1.1', '2606:4700:4700::1111'],
              'mtu': 1280
            }
          ]
        }
      ],
      'unavailable': [],
    };

WireGuardProfile parseProfile(Map<String, dynamic> value, {DateTime? now}) =>
    WireGuardProfile.parseResponse(value,
        nodeId: '1',
        identity: '8302571644:123',
        clientPublicKey: clientPublic,
        now: now ?? profileNow);

void main() {
  test('只把当前设备当前节点的公开资料送往原生，校准时钟保留有效时间', () {
    final profile = parseProfile(profileResponse());
    expect(profile.identity, '8302571644:123');
    expect(
        profile.nativeArguments(3).keys,
        unorderedEquals([
          'identity',
          'generation',
          'nodeId',
          'protocol',
          'endpoint',
          'serverPublicKey',
          'address',
          'dns',
          'mtu',
          'expiresAt',
        ]));
    expect(profile.nativeArguments(3)['generation'], 3);
    expect(() => profile.dns.add('8.8.8.8'), throwsUnsupportedError);
    final corrected = WireGuardProfile.parseResponse(profileResponse(),
        nodeId: '1',
        identity: '8302571644:123',
        clientPublicKey: clientPublic,
        now: profileNow.subtract(const Duration(hours: 1)),
        clockOffsetSeconds: 3600);
    expect(
        corrected.expiresAt
            .difference(profileNow.subtract(const Duration(hours: 1))),
        const Duration(minutes: 5));
  });

  test('拒绝过期、跨节点、私钥或未知字段、混入模拟协议及畸形网络配置', () {
    final changes = <void Function(Map<String, dynamic>)>[
      (j) => j['expires_at'] = profileNow.toIso8601String(),
      (j) => j['expires_at'] =
          profileNow.add(const Duration(minutes: 6)).toIso8601String(),
      (j) => j['expires_at'] = '2026-09-17T12:05:00',
      (j) => j['private_key'] = 'must-never-be-consumed',
      (j) => j['profiles'][0]['node_id'] = '2',
      (j) => j['profiles'].add(j['profiles'][0]),
      (j) => j['unavailable'].add({'node_id': '1', 'reason': 'OFFLINE'}),
      (j) =>
          j['profiles'][0]['outbounds'].add(j['profiles'][0]['outbounds'][0]),
      (j) => j['profiles'][0]['outbounds'][0]['protocol'] = 'MOCK',
      (j) => j['profiles'][0]['outbounds'][0]['private_key'] = 'forbidden',
      (j) =>
          j['profiles'][0]['outbounds'][0]['server_public_key'] = clientPublic,
      (j) => j['profiles'][0]['outbounds'][0]['server_public_key'] =
          base64Encode(List<int>.filled(32, 0)),
      (j) => j['profiles'][0]['outbounds'][0]['server_public_key'] = 'bad',
      (j) => j['profiles'][0]['outbounds'][0]['endpoint'] =
          'user@vpn.example.com:51820',
      (j) => j['profiles'][0]['outbounds'][0]['endpoint'] =
          'vpn.example.com:65536',
      (j) => j['profiles'][0]['outbounds'][0]['endpoint'] = 'vpn.example.com:0',
      (j) => j['profiles'][0]['outbounds'][0]['endpoint'] =
          'vpn.example.com:51820\nPrivateKey=bad',
      (j) => j['profiles'][0]['outbounds'][0]['endpoint'] = '999.1.2.3:51820',
      (j) => j['profiles'][0]['outbounds'][0]['address'] = '10.20.0.16/24',
      (j) => j['profiles'][0]['outbounds'][0]['address'] = '8.8.8.8/32',
      (j) => j['profiles'][0]['outbounds'][0]['dns'] = [],
      (j) => j['profiles'][0]['outbounds'][0]['dns'] = ['resolver.example.com'],
      (j) => j['profiles'][0]['outbounds'][0]['dns'] = ['1.1.1.1', '1.1.1.1'],
      (j) => j['profiles'][0]['outbounds'][0]['dns'] = ['1::2::3'],
      (j) => j['profiles'][0]['outbounds'][0]['mtu'] = 1279,
      (j) => j['profiles'][0]['outbounds'][0]['mtu'] = 1501,
      (j) => j['profiles'][0]['outbounds'][0]['mtu'] = '1280',
    ];
    for (var i = 0; i < changes.length; i++) {
      final response = profileResponse();
      changes[i](response);
      expect(() => parseProfile(response), throwsFormatException,
          reason: 'mutation $i');
    }
  });
}
