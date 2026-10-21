import 'dart:async';
import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/platform/vpn/egress_profile.dart';
import 'package:yuhai_vpn/platform/vpn/native_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

final egressNow = DateTime.utc(2026, 9, 28, 1);
const identity = '8302571644:123';

Map<String, dynamic> egressConfig() => {
      'dns': {
        'queryStrategy': 'UseIPv4',
        'servers': ['https://1.1.1.1/dns-query']
      },
      'inbounds': [
        {
          'port': 0,
          'protocol': 'tun',
          'settings': {'mtu': 1500, 'name': 'xray0'},
          'tag': 'tun-in'
        }
      ],
      'log': {'loglevel': 'warning'},
      'outbounds': [
        {
          'protocol': 'http',
          'settings': {
            'address': '203.0.113.11',
            'port': 3128,
            'user': 'bob',
            'pass': 'HTTPPASSWORD'
          },
          'tag': 'proxy'
        },
        {'protocol': 'freedom', 'tag': 'direct'},
      ],
      'routing': {'domainStrategy': 'AsIs', 'rules': []},
    };

Map<String, dynamic> egressResponse({Map<String, dynamic>? patch}) => {
      'egress_id': 'e2',
      'protocol': 'HTTP',
      'supports_udp': false,
      'core': 'xray',
      'core_version': '26.9.9',
      'expires_at': '2026-10-28T01:00:00Z',
      'config': egressConfig(),
      'notice': '该出口不支持 UDP：网页、聊天可用；部分游戏、语音通话与 QUIC 会改走 TCP 或不可用。',
      ...?patch,
    };

EgressProfile parseEgressProfile(Map<String, dynamic> response) =>
    EgressProfile.parseResponse(response,
        egressId: 'e2', identity: identity, now: egressNow);

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('出口连接资料', () {
    test('服务端形状的资料被接受，配置原样传给原生层', () {
      final p = parseEgressProfile(egressResponse());
      expect(p.nodeId, 'e2');
      expect(p.protocol, 'HTTP');
      expect(p.supportsUdp, isFalse);
      expect(p.notice, contains('UDP'));
      final args = p.nativeArguments(5);
      expect(args.keys.toSet(), {
        'identity',
        'generation',
        'egressId',
        'protocol',
        'config',
        'expiresAt'
      });
      expect(jsonDecode(args['config'] as String), egressConfig());
      expect(args['expiresAt'], '2026-10-28T01:00:00.000Z');
      // 调试输出不能带出凭据。
      expect(p.toString(), isNot(contains('HTTPPASSWORD')));
    });

    test('形状不对、过期、篡改或危险配置一律拒绝', () {
      final cfg = egressConfig();
      final bad = <Map<String, dynamic>>[
        {'egress_id': 'e3'},
        {'core': 'sing-box'},
        {'protocol': 'TUIC'},
        {'protocol': 'WIREGUARD'},
        {'supports_udp': 'no'},
        {'supports_udp': true}, // HTTP 代理不可能支持 UDP
        {'expires_at': '2026-09-28T00:59:59Z'},
        {'expires_at': '2028-09-28T01:00:00Z'},
        {'expires_at': '2026-10-28 01:00:00'},
        {'config': 'x'},
        {
          'config': {
            ...cfg,
            'inbounds': [
              {'listen': '0.0.0.0', 'port': 1080, 'protocol': 'socks'}
            ]
          }
        },
        {
          'config': {
            ...cfg,
            'inbounds': [
              {'listen': '127.0.0.1', 'port': 0, 'protocol': 'tun'}
            ]
          }
        },
        {
          'config': {
            ...cfg,
            'outbounds': [
              {
                'protocol': 'trojan',
                'tag': 'proxy',
                'streamSettings': {
                  'tlsSettings': {'allowInsecure': true}
                }
              }
            ]
          }
        },
        {
          'config': {
            ...cfg,
            'outbounds': [
              {'protocol': 'freedom', 'tag': 'direct'}
            ]
          }
        },
      ];
      for (final patch in bad) {
        expect(() => parseEgressProfile(egressResponse(patch: patch)),
            throwsFormatException,
            reason: '$patch');
      }
      final extra = egressResponse()..['server'] = '203.0.113.11';
      expect(() => parseEgressProfile(extra), throwsFormatException);
      expect(
          () => EgressProfile.parseResponse(egressResponse(),
              egressId: '2', identity: identity, now: egressNow),
          throwsFormatException);
    });
  });

  group('线路列表中的出口', () {
    Map<String, dynamic> item(Map<String, dynamic> patch) => {
          'id': 'e1',
          'name': '东京住宅',
          'country_code': 'JP',
          'city': '东京',
          'region': 'ASIA',
          'protocol': 'SOCKS5',
          'supports_udp': true,
          'capacity_users': 0,
          'available': true,
          'locked_reason': null,
          ...patch,
        };

    test('可用出口在安卓可选，在其他平台如实标为不支持', () {
      final list = parseEgress({
        'egress': [
          item({}),
          item({
            'id': 'e2',
            'available': false,
            'locked_reason': 'TIER',
            'protocol': 'HTTP',
            'supports_udp': false
          }),
        ]
      }, platformSupported: true);
      expect(list.first.isEgress, isTrue);
      expect(list.first.available, isTrue);
      expect(list.first.protocols, ['SOCKS5']);
      expect(list.first.probeHost, isNull); // 出口不公开主机
      expect(list.last.available, isFalse);
      expect(list.last.unavailableLabel, '需要更高等级套餐');

      final other = parseEgress({
        'egress': [item({})]
      }, platformSupported: false);
      expect(other.single.available, isFalse);
      expect(other.single.lockedReason, 'PLATFORM');
      expect(list.first.capacityUsers, 0);
      expect(list.first.isSaturated, isFalse);
    });

    test('现网出口缺少 capacity_users 时仍可选线，人数上限按未知', () {
      final current = item({})..remove('capacity_users');
      final missing = parseEgress({
        'egress': [current]
      }, platformSupported: true);
      expect(missing.single.available, isTrue);
      expect(missing.single.capacityUsers, isNull);
      expect(missing.single.isSaturated, isFalse);
      expect(missing.single.protocols, ['SOCKS5']);
      expect(missing.single.id, 'e1');

      final explicitNull = parseEgress({
        'egress': [
          item({'capacity_users': null})
        ]
      }, platformSupported: true);
      expect(explicitNull.single.capacityUsers, isNull);
      expect(explicitNull.single.available, isTrue);

      final capped = parseEgress({
        'egress': [
          item({'capacity_users': 40})
        ]
      }, platformSupported: true);
      expect(capped.single.capacityUsers, 40);
      expect(capped.single.isSaturated, isFalse);

      for (final bad in <Object>[-1, 1000001, '0', 1.5, true]) {
        expect(
            () => parseEgress({
                  'egress': [
                    item({'capacity_users': bad})
                  ]
                }, platformSupported: true),
            throwsA(isA<FormatException>()
                .having((error) => error.message, 'message', '线路资料无效')),
            reason: '$bad');
      }
    });

    test('畸形出口条目整体拒绝，不静默丢弃', () {
      for (final patch in <Map<String, dynamic>>[
        {'id': '1'},
        {'id': 'e01'},
        {'country_code': 'jp'},
        {'available': true, 'locked_reason': 'TIER'},
        {'available': false, 'locked_reason': null},
        {'protocol': 'TUIC'},
        {'host': '203.0.113.1'},
        {'name': ''},
      ]) {
        expect(
            () => parseEgress({
                  'egress': [item(patch)]
                }, platformSupported: true),
            throwsFormatException,
            reason: '$patch');
      }
      expect(
          () => parseEgress({
                'egress': [item({}), item({})]
              }, platformSupported: true),
          throwsFormatException);
    });
  });

  group('原生引擎的出口连接', () {
    const channel = MethodChannel('test/yuhai/vpn-egress');
    late StreamController<dynamic> events;
    late NativeVpnEngine engine;
    late List<MethodCall> calls;
    late List<VpnStatus> statuses;
    setUp(() {
      calls = [];
      statuses = [];
      events = StreamController<dynamic>.broadcast();
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, (call) async {
        calls.add(call);
        return call.method == 'prepare' ? true : null;
      });
      var next = 0;
      engine = NativeVpnEngine(
          nextGeneration: () => ++next,
          methodChannel: channel,
          events: events.stream,
          now: () => egressNow);
      engine.status.listen(statuses.add);
    });
    tearDown(() async {
      engine.dispose();
      await events.close();
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, null);
    });

    test('走 connectXray，只有经出口的真实请求确认后才算已连接', () async {
      await engine.connect(parseEgressProfile(egressResponse()));
      await Future<void>.delayed(Duration.zero);
      expect(calls.last.method, 'connectXray');
      expect(calls.last.arguments['egressId'], 'e2');
      expect(statuses, [VpnStatus.connecting]);
      // 没有确认时间的「已连接」不采信
      events.add({
        'identity': identity,
        'generation': 1,
        'type': 'status',
        'status': 'connected'
      });
      await Future<void>.delayed(Duration.zero);
      expect(statuses.last, VpnStatus.connecting);
      events.add({
        'identity': identity,
        'generation': 1,
        'type': 'status',
        'status': 'connected',
        'handshakeConfirmed': true,
        'handshakeAt': egressNow.toIso8601String()
      });
      await Future<void>.delayed(Duration.zero);
      expect(statuses.last, VpnStatus.connected);
    });

    test('被系统接管时如实报错', () async {
      final failures = <VpnFailure>[];
      engine.failures.listen(failures.add);
      await engine.connect(parseEgressProfile(egressResponse()));
      events.add({
        'identity': identity,
        'generation': 1,
        'type': 'error',
        'code': 'VPN_REVOKED'
      });
      await Future<void>.delayed(Duration.zero);
      expect(failures.single.code, 'VPN_REVOKED');
      expect(statuses.last, VpnStatus.error);
    });
  });
}
