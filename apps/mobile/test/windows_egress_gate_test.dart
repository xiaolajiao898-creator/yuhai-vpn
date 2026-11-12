import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/providers.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_controller.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_engine.dart';

import 'connection_guard_test.dart' show demoNode;
import 'session_lifecycle_test.dart' show TestDeviceKey, clientFor, login;

/// 连接控制器只把 [WireGuardVpnEngine] 当作可连接引擎。
class _RecordingEngine implements WireGuardVpnEngine {
  final profiles = <ConnectProfile>[];
  var preparations = 0;

  @override
  bool get canProbe => true;

  @override
  Stream<VpnStatus> get status => const Stream.empty();

  @override
  Stream<VpnStats> get stats => const Stream.empty();

  @override
  Stream<VpnFailure> get failures => const Stream.empty();

  @override
  Future<bool> prepare() async {
    preparations++;
    return true;
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
  }

  @override
  Future<void> disconnect() async {}

  @override
  Future<int?> probe(String host, int port,
          {Duration timeout = const Duration(seconds: 2)}) async =>
      null;

  @override
  bool get canRecover => false;
  @override
  Future<void> applyPolicy({required bool leakProtect}) async {}
  @override
  Future<int?> probeTunnel() async => null;
  @override
  Future<bool> holdTraffic() async => false;
  @override
  Future<void> releaseTraffic() async {}
  @override
  Future<int> networkEpoch() async => 0;

  @override
  Future<String> publicKey(String identity) async => 'test-public-key';

  void dispose() {}
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  setUp(() {
    FlutterSecureStorage.setMockInitialValues({});
  });

  test('Windows 放行出口，引擎是 Xray，没有 WireGuard 节点时不启动它', () async {
    debugDefaultTargetPlatformOverride = TargetPlatform.windows;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    expect(egressPlatformSupported, isTrue);

    final container = ProviderContainer();
    addTearDown(container.dispose);
    final engine = container.read(vpnEngineProvider);
    expect(engine, isA<WindowsXrayEngine>());
    expect(engine, isNot(isA<WindowsWireGuardEngine>()));

    final store = container.read(sessionStoreProvider);
    await store.saveSession(login('123'));
    await store.saveDeviceUid('device-uid');
    final recorded = _RecordingEngine();
    final gated = ProviderContainer(overrides: [
      sessionStoreProvider.overrideWithValue(store),
      apiClientProvider.overrideWithValue(clientFor(store, TestDeviceKey())),
      vpnEngineProvider.overrideWithValue(recorded),
      nodesProvider.overrideWith((ref) async => [
            const NodeItem(
              id: 'e2',
              name: '出口',
              countryCode: 'JP',
              city: '东京',
              region: 'ASIA',
              available: true,
              isRecommended: false,
              protocols: ['VLESS'],
              supportsUdp: true,
            )
          ]),
    ]);
    addTearDown(gated.dispose);
    addTearDown(recorded.dispose);
    await gated.read(sessionProvider.future);
    await gated.read(connectionControllerProvider.notifier).toggle();
    expect(recorded.profiles, isEmpty);
    expect(recorded.preparations, 0);
    expect(gated.read(connectionFailureProvider)!.code, 'ENGINE_UNAVAILABLE');
    expect(gated.read(connectionFailureProvider)!.message, '请先选择一条出口线路');
    expect(windowsWireGuardSelectable([demoNode('1', protocol: 'WIREGUARD')]),
        isTrue);
    expect(
        windowsWireGuardSelectable(const [
          NodeItem(
            id: 'e2',
            name: '出口',
            countryCode: 'JP',
            city: '',
            region: 'ASIA',
            available: true,
            isRecommended: false,
            protocols: ['VLESS'],
          )
        ]),
        isFalse);
  });

  test('安卓仍然放行出口，Linux 不放行', () {
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    expect(egressPlatformSupported, isTrue);
    debugDefaultTargetPlatformOverride = TargetPlatform.linux;
    expect(egressPlatformSupported, isFalse);
  });
}
