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
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

class RecordingEngine extends MockVpnEngine {
  final profiles = <ConnectProfile>[];
  int preparations = 0, disconnections = 0;
  Completer<bool>? permission;
  @override
  Future<bool> prepare() {
    preparations++;
    return permission?.future ?? Future.value(true);
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
  }

  @override
  Future<void> disconnect() async {
    disconnections++;
  }

  @override
  Future<int?> probe(String host, int port,
          {Duration timeout = const Duration(seconds: 2)}) async =>
      host.startsWith('fast') ? 10 : 50;
}

NodeItem demoNode(String id,
        {bool available = true, String protocol = 'MOCK'}) =>
    NodeItem(
      id: id,
      name: id,
      countryCode: 'HK',
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
  test('模拟引擎明确拒绝真实协议', () async {
    final engine = MockVpnEngine();
    await engine.connect(const ConnectProfile(
        nodeId: '1', protocol: 'WIREGUARD', server: 'node.invalid'));
    expect(engine.current, VpnStatus.error);
    engine.dispose();
  });
}
