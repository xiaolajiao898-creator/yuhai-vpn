import 'dart:async';
import 'dart:math';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/unavailable_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/tunnel_diagnostic.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

class RecordingEngine extends MockVpnEngine {
  final profiles = <ConnectProfile>[];
  int preparations = 0, disconnections = 0;
  Completer<bool>? permission;
  Future<int?> Function(String host)? probeOverride;
  Future<void> Function(ConnectProfile profile)? connectOverride;
  @override
  Future<bool> prepare() {
    preparations++;
    return permission?.future ?? Future.value(true);
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
    final hook = connectOverride;
    if (hook != null) await hook(profile);
  }

  @override
  Future<void> disconnect() async {
    disconnections++;
  }

  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) async {
    final hook = probeOverride;
    if (hook != null) return hook(host);
    return host.startsWith('fast') ? 10 : 50;
  }
}

class _SwitchingEngine extends MockVpnEngine implements HandshakeReporter {
  _SwitchingEngine() : super(random: Random(1));

  final profiles = <ConnectProfile>[];
  final _statusSync = StreamController<VpnStatus>.broadcast(sync: true);
  final _failSync = StreamController<VpnFailure>.broadcast(sync: true);
  var failIds = <String>{};

  @override
  bool get reportsHandshake => true;

  @override
  TunnelDiagnostic? get tunnelDiagnostic => null;

  @override
  Stream<VpnStatus> get status => _statusSync.stream;

  @override
  Stream<VpnFailure> get failures => _failSync.stream;

  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
    if (failIds.contains(profile.nodeId)) {
      _failSync.add(const VpnFailure('HANDSHAKE_TIMEOUT', '线路握手超时，请重试或更换线路'));
      _statusSync.add(VpnStatus.error);
      return;
    }
    _statusSync.add(VpnStatus.connected);
  }

  @override
  Future<void> disconnect() async {}

  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) async {
    return host.startsWith('a') ? 10 : 40;
  }

  @override
  void dispose() {
    _statusSync.close();
    _failSync.close();
    super.dispose();
  }
}

NodeItem demoNode(String id,
        {bool available = true, String protocol = 'MOCK', String countryCode = 'HK'}) =>
    NodeItem(
      id: id,
      name: id,
      countryCode: countryCode,
      city: '香港',
      region: 'ASIA',
      available: available,
      lockedReason: available ? null : 'OFFLINE',
      load: available ? 20 : null,
      isRecommended: false,
      probeHost: available ? '$id.invalid' : null,
      probePort: available ? 443 : null,
      protocols: available ? [protocol] : [],
    );

class LateRandom implements Random {
  @override
  int nextInt(int max) => max - 1;
  @override
  bool nextBool() => false;
  @override
  double nextDouble() => .99;
}

void main() {
  test('不支持原生 VPN 的平台没有模拟引擎，也不会产生假的延迟', () async {
    debugDefaultTargetPlatformOverride = TargetPlatform.linux;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    final container = ProviderContainer();
    addTearDown(container.dispose);
    final engine = container.read(vpnEngineProvider);
    expect(engine, isA<UnavailableVpnEngine>());
    expect(engine.canProbe, isFalse);
    expect(await engine.prepare(), isFalse);
    expect(await engine.probe('node.invalid', 443), isNull);
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(container.read(connectionControllerProvider), VpnStatus.error);
    expect(
        container.read(connectionFailureProvider)!.code, 'ENGINE_UNAVAILABLE');
  });
  test('手选线路用于演示配置，自动模式选择本次测速最快线路', () async {
    for (final selected in <String?>['slow', null]) {
      final engine = RecordingEngine();
      var reads = 0;
      final container = ProviderContainer(overrides: [
        vpnDemoModeProvider.overrideWithValue(true),
        vpnEngineProvider.overrideWithValue(engine),
        nodesProvider.overrideWith((ref) async {
          reads++;
          return [demoNode('slow'), demoNode('fast')];
        }),
      ]);
      container.read(selectedNodeProvider.notifier).state = selected;
      await container.read(connectionControllerProvider.notifier).toggle();
      expect(engine.profiles.single.nodeId, selected ?? 'fast');
      expect(engine.profiles.single.protocol, 'MOCK');
      expect(reads, 2);
      container.dispose();
      engine.dispose();
    }
  });
  test('延迟全部失败时按服务端顺序试连，失败再试下一条，最多 3 条', () async {
    final ok = RecordingEngine();
    ok.probeOverride = (_) async => null;
    final container = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(ok),
      nodesProvider.overrideWith((ref) async => [
            demoNode('a'),
            demoNode('b'),
            demoNode('c'),
            demoNode('d'),
          ]),
    ]);
    container.read(selectedNodeProvider.notifier).state = null;
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(ok.profiles.map((p) => p.nodeId).toList(), ['a']);
    expect(container.read(connectionFailureProvider), isNull);
    container.dispose();
    ok.dispose();

    final skipped = RecordingEngine();
    skipped.probeOverride = (host) async => host.startsWith('b') ? 12 : null;
    final picked = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(skipped),
      nodesProvider.overrideWith((ref) async => [
            demoNode('a'),
            demoNode('b'),
            demoNode('c'),
          ]),
    ]);
    picked.read(selectedNodeProvider.notifier).state = null;
    await picked.read(connectionControllerProvider.notifier).toggle();
    expect(skipped.profiles.map((p) => p.nodeId).toList(), ['b']);
    picked.dispose();
    skipped.dispose();

    final retry = RecordingEngine();
    retry.probeOverride = (_) async => null;
    retry.connectOverride = (profile) async {
      if (profile.nodeId == 'a' || profile.nodeId == 'b') {
        throw const VpnFailure('CONNECT_FAILED', '这条没连上');
      }
    };
    final retried = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(retry),
      nodesProvider.overrideWith((ref) async => [
            demoNode('a'),
            demoNode('b'),
            demoNode('c'),
            demoNode('d'),
          ]),
    ]);
    retried.read(selectedNodeProvider.notifier).state = null;
    await retried.read(connectionControllerProvider.notifier).toggle();
    expect(retry.profiles.map((p) => p.nodeId).toList(), ['a', 'b', 'c']);
    expect(retry.disconnections, 2);
    expect(retried.read(connectionFailureProvider), isNull);
    retried.dispose();
    retry.dispose();

    final exhausted = RecordingEngine();
    exhausted.probeOverride = (_) async => null;
    exhausted.connectOverride = (profile) async {
      throw const VpnFailure('CONNECT_FAILED', '这条没连上');
    };
    final gaveUp = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(exhausted),
      nodesProvider.overrideWith((ref) async => [
            demoNode('a'),
            demoNode('b'),
            demoNode('c'),
            demoNode('d'),
          ]),
    ]);
    gaveUp.read(selectedNodeProvider.notifier).state = null;
    await gaveUp.read(connectionControllerProvider.notifier).toggle();
    expect(exhausted.profiles.map((p) => p.nodeId).toList(), ['a', 'b', 'c']);
    expect(gaveUp.read(connectionFailureProvider)?.code, 'CONNECT_FAILED');
    expect(gaveUp.read(connectionFailureProvider)?.message, '这条没连上');
    gaveUp.dispose();
    exhausted.dispose();

    final fatal = RecordingEngine();
    fatal.probeOverride = (_) async => null;
    fatal.connectOverride = (profile) async {
      throw const VpnFailure('AUTH_REQUIRED', '请先登录账户');
    };
    final stopped = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(fatal),
      nodesProvider.overrideWith((ref) async => [
            demoNode('a'),
            demoNode('b'),
            demoNode('c'),
          ]),
    ]);
    stopped.read(selectedNodeProvider.notifier).state = null;
    await stopped.read(connectionControllerProvider.notifier).toggle();
    expect(fatal.profiles.map((p) => p.nodeId).toList(), ['a']);
    expect(stopped.read(connectionFailureProvider)?.code, 'AUTH_REQUIRED');
    stopped.dispose();
    fatal.dispose();
  });
  test('第二次核对发现节点下线不能连接，真实协议也不能送入模拟引擎', () async {
    for (final realProtocol in [false, true]) {
      final engine = RecordingEngine();
      var reads = 0;
      final container = ProviderContainer(overrides: [
        vpnDemoModeProvider.overrideWithValue(true),
        vpnEngineProvider.overrideWithValue(engine),
        nodesProvider.overrideWith((ref) async {
          reads++;
          return [
            demoNode('slow',
                available: reads == 1,
                protocol: realProtocol ? 'WIREGUARD' : 'MOCK')
          ];
        }),
      ]);
      container.read(selectedNodeProvider.notifier).state = 'slow';
      await container.read(connectionControllerProvider.notifier).toggle();
      expect(engine.profiles, isEmpty);
      expect(engine.preparations, realProtocol ? 0 : 1);
      expect(container.read(connectionControllerProvider), VpnStatus.error);
      container.dispose();
      engine.dispose();
    }
  });
  test('取消和销毁后迟到的权限结果不能重新连接', () async {
    for (final dispose in [false, true]) {
      final engine = RecordingEngine()..permission = Completer<bool>();
      final container = ProviderContainer(overrides: [
        vpnDemoModeProvider.overrideWithValue(true),
        vpnEngineProvider.overrideWithValue(engine),
        nodesProvider.overrideWith((ref) async => [demoNode('slow')]),
      ]);
      container.read(selectedNodeProvider.notifier).state = 'slow';
      final controller = container.read(connectionControllerProvider.notifier);
      final first = controller.toggle();
      for (var i = 0; i < 20 && engine.preparations == 0; i++) {
        await Future<void>.delayed(Duration.zero);
      }
      expect(engine.preparations, 1);
      if (dispose) {
        container.dispose();
      } else {
        await controller.toggle();
        expect(container.read(connectionControllerProvider),
            VpnStatus.disconnected);
      }
      engine.permission!.complete(true);
      await first;
      expect(engine.profiles, isEmpty);
      if (!dispose) container.dispose();
      engine.dispose();
    }
  });
  test('权限等待期间更换线路，旧选择不会建立连接', () async {
    final engine = RecordingEngine()..permission = Completer<bool>();
    final container = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(engine),
      nodesProvider
          .overrideWith((ref) async => [demoNode('slow'), demoNode('fast')]),
    ]);
    container.read(selectedNodeProvider.notifier).state = 'slow';
    final pending =
        container.read(connectionControllerProvider.notifier).toggle();
    for (var i = 0; i < 20 && engine.preparations == 0; i++) {
      await Future<void>.delayed(Duration.zero);
    }
    expect(engine.preparations, 1);
    container.read(selectedNodeProvider.notifier).state = 'fast';
    engine.permission!.complete(true);
    await pending;
    expect(engine.profiles, isEmpty);
    expect(container.read(connectionControllerProvider), VpnStatus.error);
    container.dispose();
    engine.dispose();
  });
  testWidgets('旧模拟握手不能抢先完成新连接，销毁后无迟到回调', (tester) async {
    final engine = MockVpnEngine(random: LateRandom());
    const profile =
        ConnectProfile(nodeId: '1', protocol: 'MOCK', server: 'demo.invalid');
    final first = engine.connect(profile);
    await tester.pump(const Duration(milliseconds: 500));
    final stop = engine.disconnect();
    await tester.pump(const Duration(milliseconds: 300));
    await stop;
    final second = engine.connect(profile);
    await tester.pump(const Duration(milliseconds: 1199));
    await first;
    expect(engine.current, VpnStatus.connecting);
    await tester.pump(const Duration(milliseconds: 800));
    await second;
    expect(engine.current, VpnStatus.connected);
    engine.dispose();
    await tester.pump(const Duration(seconds: 3));
    expect(tester.takeException(), isNull);
  });
  test('握手超时换同一地区的下一条，不跨地区', () async {
    final same = _SwitchingEngine()..failIds = {'a'};
    final container = ProviderContainer(overrides: [
      vpnDemoModeProvider.overrideWithValue(true),
      vpnEngineProvider.overrideWithValue(same),
      nodesProvider.overrideWith((ref) async => [demoNode('a'), demoNode('b')]),
    ]);
    addTearDown(same.dispose);
    addTearDown(container.dispose);
    container.read(selectedNodeProvider.notifier).state = null;
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(same.profiles.map((p) => p.nodeId).toList(), ['a', 'b']);
    expect(container.read(connectionControllerProvider), VpnStatus.connected);
    expect(container.read(connectionNoticeProvider), '已改连 b');
    expect(container.read(selectedNodeProvider), 'b');

    final other = _SwitchingEngine()..failIds = {'a'};
    final blocked = ProviderContainer(overrides: [
      vpnEngineProvider.overrideWithValue(other),
      vpnDemoModeProvider.overrideWithValue(true),
      nodesProvider.overrideWith((ref) async =>
          [demoNode('a'), demoNode('b', countryCode: 'JP')]),
    ]);
    addTearDown(other.dispose);
    addTearDown(blocked.dispose);
    blocked.read(selectedNodeProvider.notifier).state = null;
    await blocked.read(connectionControllerProvider.notifier).toggle();
    expect(other.profiles.map((p) => p.nodeId).toList(), ['a']);
    expect(blocked.read(connectionControllerProvider), VpnStatus.error);
    expect(blocked.read(connectionFailureProvider)!.code, 'HANDSHAKE_TIMEOUT');
  });
  test('模拟引擎明确拒绝真实协议', () async {
    final engine = MockVpnEngine();
    await engine.connect(const ConnectProfile(
        nodeId: '1', protocol: 'WIREGUARD', server: 'node.invalid'));
    expect(engine.current, VpnStatus.error);
    engine.dispose();
  });
}
