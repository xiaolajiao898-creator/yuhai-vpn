import 'dart:async';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/native_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

import 'wireguard_profile_test.dart'
    show parseProfile, profileResponse, profileNow, clientPublic;

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const channel = MethodChannel('test/yuhai/vpn');
  late StreamController<dynamic> events;
  late NativeVpnEngine engine;
  late List<MethodCall> calls;
  late List<VpnStatus> statuses;
  late List<VpnStats> statistics;
  late List<VpnFailure> failures;
  setUp(() {
    calls = [];
    statuses = [];
    statistics = [];
    failures = [];
    events = StreamController<dynamic>.broadcast();
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, (call) async {
      calls.add(call);
      return switch (call.method) {
        'publicKey' => clientPublic,
        'prepare' => true,
        'probe' => 17,
        _ => null
      };
    });
    var nextGeneration = 0;
    engine = NativeVpnEngine(
        nextGeneration: () => ++nextGeneration,
        methodChannel: channel,
        events: events.stream,
        now: () => profileNow);
    engine.status.listen(statuses.add);
    engine.stats.listen(statistics.add);
    engine.failures.listen(failures.add);
  });
  tearDown(() async {
    engine.dispose();
    await events.close();
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, null);
  });
  Future<void> event(Map<String, dynamic> value) async {
    events.add({'identity': '8302571644:123', 'generation': 1, ...value});
    await Future<void>.delayed(Duration.zero);
  }

  test('原生接收配置不等于已连接，只有当前身份和本次握手可置已连接', () async {
    expect(await engine.publicKey('8302571644:123'), clientPublic);
    expect(await engine.prepare(), isTrue);
    await engine.connect(parseProfile(profileResponse()));
    await Future<void>.delayed(Duration.zero);
    expect(statuses, [VpnStatus.connecting]);
    expect(calls.last.arguments['protocol'], 'WIREGUARD');
    expect(
        calls.last.arguments.keys.any(
            (dynamic key) => key.toString().toLowerCase().contains('private')),
        isFalse);
    await event({'type': 'status', 'status': 'connected'});
    await event({'status': 'connected', 'handshakeConfirmed': true});
    await event({
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt':
          profileNow.subtract(const Duration(minutes: 1)).toIso8601String()
    });
    await event({
      'generation': 0,
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    await event({
      'identity': 'other:123',
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    expect(statuses, [VpnStatus.connecting]);
    await event({
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    expect(statuses.last, VpnStatus.connected);
    await event({
      'type': 'stats',
      'upBytes': 100,
      'downBytes': 200,
      'upSpeed': 10,
      'downSpeed': 20,
      'since': profileNow.millisecondsSinceEpoch
    });
    expect(statistics.single.downBytes, 200);
    await engine.disconnect();
    await event({
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    expect(statuses.last, VpnStatus.disconnected);
  });

  test('断开后再连不接受旧代事件或统计，原生故障信息不透传配置', () async {
    await engine.connect(parseProfile(profileResponse()));
    await engine.disconnect();
    await engine.connect(parseProfile(profileResponse()));
    await event({
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    await event({
      'type': 'stats',
      'upBytes': 1,
      'downBytes': 2,
      'upSpeed': 1,
      'downSpeed': 2,
      'since': profileNow.millisecondsSinceEpoch
    });
    expect(statuses.last, VpnStatus.connecting);
    expect(statistics, isEmpty);
    await event({
      'generation': 3,
      'type': 'error',
      'code': 'NATIVE_DETAILS',
      'message': 'private config must not reach UI'
    });
    expect(statuses.last, VpnStatus.error);
    expect(failures.single.message, isNot(contains('private')));
  });

  test('原生引擎拒绝模拟配置，探测返回真实原生结果', () async {
    await expectLater(
        engine.connect(const ConnectProfile(
            nodeId: '1', protocol: 'MOCK', server: 'demo')),
        throwsFormatException);
    expect(calls, isEmpty);
    expect(await engine.probe('vpn.example.com', 443), 17);
    expect(await engine.probe('evil\nhost', 443), isNull);
    expect(await engine.probe('vpn.example.com', 0), isNull);
  });

  testWidgets('握手超时触发真实断开并报告失败，迟到握手无效', (tester) async {
    await engine.connect(parseProfile(profileResponse()));
    await tester.pump(const Duration(seconds: 30));
    await tester.pump();
    expect(calls.last.method, 'disconnect');
    expect(failures.single.code, 'HANDSHAKE_TIMEOUT');
    events.add({
      'identity': '8302571644:123',
      'generation': 1,
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    await tester.pump();
    expect(statuses.last, VpnStatus.error);
  });
  test('重建原生引擎后代次仍递增', () async {
    final a = StreamController<dynamic>.broadcast();
    final b = StreamController<dynamic>.broadcast();
    final first = NativeVpnEngine(
        methodChannel: channel, events: a.stream, now: () => profileNow);
    await first.connect(parseProfile(profileResponse()));
    final g1 = (calls.last.arguments as Map)['generation'] as int;
    first.dispose();
    await Future<void>.delayed(Duration.zero);
    final g2 = (calls.last.arguments as Map)['generation'] as int;
    final second = NativeVpnEngine(
        methodChannel: channel, events: b.stream, now: () => profileNow);
    await second.connect(parseProfile(profileResponse()));
    final g3 = (calls.last.arguments as Map)['generation'] as int;
    expect(g2, greaterThan(g1));
    expect(g3, greaterThan(g2));
    second.dispose();
    await a.close();
    await b.close();
  });
  test('断开等待原生完成，等待期间迟到握手不能恢复 connected', () async {
    final stopped = Completer<void>();
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(channel, (call) async {
      calls.add(call);
      if (call.method == 'disconnect') await stopped.future;
      return null;
    });
    await engine.connect(parseProfile(profileResponse()));
    var complete = false;
    final disconnect = engine.disconnect().then((_) => complete = true);
    await event({
      'status': 'connected',
      'handshakeConfirmed': true,
      'handshakeAt': profileNow.toIso8601String()
    });
    expect(complete, isFalse);
    expect(statuses.last, VpnStatus.disconnecting);
    stopped.complete();
    await disconnect;
    await Future<void>.delayed(Duration.zero);
    expect(statuses.last, VpnStatus.disconnected);
  });

  testWidgets('generation 分配有间隙时握手超时仍必须上报', (tester) async {
    engine.dispose();
    var seq = 100;
    engine = NativeVpnEngine(
        nextGeneration: () => seq += 10,
        methodChannel: channel,
        events: events.stream,
        now: () => profileNow);
    engine.failures.listen(failures.add);
    await engine.connect(parseProfile(profileResponse()));
    await tester.pump(const Duration(seconds: 30));
    await tester.pump();
    expect(failures.single.code, 'HANDSHAKE_TIMEOUT');
  });
}
