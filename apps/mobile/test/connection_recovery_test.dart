import 'dart:async';

import 'package:fake_async/fake_async.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';
import 'package:yuhai_vpn/features/settings/data/connection_preferences.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

class _Prefs implements ConnectionPreferencesStorage {
  _Prefs(this.value);
  ConnectionPreferences value;
  @override
  Future<ConnectionPreferences> load() async => value;
  @override
  Future<void> save(ConnectionPreferences preferences) async {
    value = preferences;
  }
}

class RecoveringEngine implements VpnEngine {
  final profiles = <ConnectProfile>[];
  int holds = 0, releases = 0;
  int? tunnelLatency = 1;
  int epoch = 1;
  bool protect = true;
  bool failSameLine = false;
  bool failAll = false;
  final _status = StreamController<VpnStatus>.broadcast(sync: true);
  final _stats = StreamController<VpnStats>.broadcast(sync: true);
  final _failures = StreamController<VpnFailure>.broadcast(sync: true);

  @override
  bool get canProbe => true;
  @override
  bool get canRecover => true;
  @override
  Stream<VpnStatus> get status => _status.stream;
  @override
  Stream<VpnStats> get stats => _stats.stream;
  @override
  Stream<VpnFailure> get failures => _failures.stream;
  @override
  Future<bool> prepare() async => true;
  @override
  Future<void> applyPolicy({required bool leakProtect}) async {
    protect = leakProtect;
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
    if (failAll || (failSameLine && profile.nodeId != 'b' && profiles.length > 1)) {
      _failures.add(const VpnFailure('NETWORK_UNAVAILABLE', '网络暂不可用，请检查网络后重试'));
      _status.add(VpnStatus.error);
      return;
    }
    _status.add(VpnStatus.connected);
  }

  @override
  Future<void> disconnect() async {
    _status.add(VpnStatus.disconnected);
  }

  @override
  Future<int?> probe(String host, int port,
          {Duration timeout = const Duration(seconds: 2)}) async =>
      10;
  @override
  Future<int?> probeTunnel() async => tunnelLatency;
  @override
  Future<bool> holdTraffic() async {
    holds++;
    return protect;
  }

  @override
  Future<void> releaseTraffic() async {
    releases++;
  }

  @override
  Future<int> networkEpoch() async => epoch;

  void revoked() {
    _failures.add(const VpnFailure('VPN_REVOKED', 'VPN 已被系统或其他 VPN 应用接管，请重新连接'));
    _status.add(VpnStatus.error);
  }

  void dispose() {
    unawaited(_status.close());
    unawaited(_stats.close());
    unawaited(_failures.close());
  }
}

NodeItem _line(String id) => NodeItem(
      id: id,
      name: id,
      countryCode: 'SG',
      city: '新加坡',
      region: 'ASIA',
      available: true,
      isRecommended: id == 'a',
      protocols: const ['MOCK'],
      probeHost: '$id.invalid',
      probePort: 443,
    );

ProviderContainer _container(RecoveringEngine engine, ConnectionPreferences prefs) {
  final container = ProviderContainer(overrides: [
    vpnDemoModeProvider.overrideWithValue(true),
    vpnEngineProvider.overrideWithValue(engine),
    nodesProvider.overrideWith((ref) async => [_line('a'), _line('b')]),
    connectionPreferencesStoreProvider.overrideWithValue(_Prefs(prefs)),
  ]);
  container.read(selectedNodeProvider.notifier).state = 'a';
  return container;
}

void _finish(dynamic async, ProviderContainer container, RecoveringEngine engine) {
  container.dispose();
  engine.dispose();
  async.elapse(const Duration(milliseconds: 20));
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('卡死两次后重连当前线路，只有断开才放开网络', () {
    fakeAsync((async) {
      final engine = RecoveringEngine();
      final container = _container(engine, const ConnectionPreferences());
      container.read(connectionPreferencesProvider.future);
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(milliseconds: 50));
      expect(container.read(connectionControllerProvider), VpnStatus.connected);
      engine.tunnelLatency = null;
      async.elapse(const Duration(seconds: 43));
      expect(engine.profiles.length, greaterThan(1));
      expect(engine.profiles.last.nodeId, 'a');
      expect(engine.holds, greaterThan(0));
      expect(container.read(connectionNoticeProvider), contains('重连中'));
      expect(container.read(trafficHeldProvider), isTrue);
      container.read(connectionControllerProvider.notifier).release();
      async.elapse(const Duration(milliseconds: 20));
      expect(engine.releases, greaterThan(0));
      expect(container.read(connectionControllerProvider), VpnStatus.disconnected);
      expect(container.read(trafficHeldProvider), isFalse);
      _finish(async, container, engine);
    });
  });

  test('同一线路连续失败后切到同一国家的另一条', () {
    fakeAsync((async) {
      final engine = RecoveringEngine()..failSameLine = true;
      final container = _container(engine, const ConnectionPreferences());
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(seconds: 70));
      expect(engine.profiles.last.nodeId, 'b');
      expect(container.read(selectedNodeProvider), 'b');
      expect(container.read(connectionControllerProvider), VpnStatus.connected);
      expect(container.read(connectionNoticeProvider), contains('已切换线路'));
      expect(container.read(connectionFailureProvider)?.code, isNot('SELECTION_CHANGED'));
      _finish(async, container, engine);
    });
  });

  test('网络切换先重连当前线路', () {
    fakeAsync((async) {
      final engine = RecoveringEngine();
      final container = _container(engine, const ConnectionPreferences());
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(seconds: 4));
      engine.epoch = 2;
      async.elapse(const Duration(seconds: 8));
      expect(engine.profiles.length, greaterThan(1));
      expect(engine.profiles.last.nodeId, 'a');
      expect(container.read(connectionNoticeProvider), contains('网络已切换'));
      _finish(async, container, engine);
    });
  });

  test('关闭自动重连时只拦住流量，不自己重连', () {
    fakeAsync((async) {
      final engine = RecoveringEngine();
      final container = _container(
          engine, const ConnectionPreferences(autoRecover: false));
      container.read(connectionPreferencesProvider.future);
      async.elapse(const Duration(milliseconds: 50));
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(milliseconds: 50));
      expect(container.read(connectionControllerProvider), VpnStatus.connected);
      engine.tunnelLatency = null;
      async.elapse(const Duration(seconds: 43));
      expect(engine.profiles, hasLength(1));
      expect(container.read(connectionControllerProvider), VpnStatus.reconnecting);
      expect(container.read(connectionNoticeProvider), contains('已保护'));
      expect(engine.holds, greaterThan(0));
      _finish(async, container, engine);
    });
  });

  test('系统撤掉 VPN 后不自动重连', () {
    fakeAsync((async) {
      final engine = RecoveringEngine();
      final container = _container(engine, const ConnectionPreferences());
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(milliseconds: 50));
      engine.revoked();
      async.elapse(const Duration(seconds: 60));
      expect(engine.profiles, hasLength(1));
      expect(container.read(connectionFailureProvider)?.code, 'VPN_REVOKED');
      _finish(async, container, engine);
    });
  });

  test('还没连上就失败时不会进入重连循环', () {
    fakeAsync((async) {
      final engine = RecoveringEngine()..failAll = true;
      final container = _container(engine, const ConnectionPreferences());
      container.read(connectionControllerProvider.notifier).toggle();
      async.elapse(const Duration(seconds: 30));
      expect(engine.profiles, hasLength(1));
      expect(container.read(connectionControllerProvider), VpnStatus.error);
      _finish(async, container, engine);
    });
  });
}
