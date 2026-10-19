import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:yuhai_vpn/app/providers.dart';
import 'package:yuhai_vpn/core/network/api_client.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_controller.dart';
import 'package:yuhai_vpn/features/settings/data/connection_preferences.dart';
import 'package:yuhai_vpn/platform/vpn/egress_profile.dart';

import 'connection_guard_test.dart' show demoNode;
import 'egress_profile_test.dart' show egressResponse;
import 'real_connection_test.dart' show MemoryPreferences, RealEngine;
import 'session_lifecycle_test.dart' show TestDeviceKey, login, reply;

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  late ProviderContainer container;
  late RealEngine engine;
  late List<String> paths;

  Future<ProviderContainer> start({
    required List<NodeItem> nodes,
    Map<String, dynamic>? me,
    String? selected,
  }) async {
    FlutterSecureStorage.setMockInitialValues({});
    SharedPreferences.setMockInitialValues({});
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    final store = SessionStore();
    await store.saveSession(login('123'));
    await store.saveDeviceUid('device-uid');
    final client = ApiClient(
      baseUrl: 'http://localhost',
      deviceKey: TestDeviceKey(),
      store: store,
      appVersion: 'test',
      platform: 'ANDROID',
    );
    paths = [];
    client.dio.interceptors.add(InterceptorsWrapper(onRequest: (request, handler) {
      paths.add(request.path);
      if (request.path == '/v1/connect/egress') {
        reply(request, handler, 200, {
          'data': egressResponse(patch: {
            'egress_id': 'e12',
            'protocol': 'VLESS',
            'supports_udp': true,
            'expires_at': '2026-11-18T03:56:00Z',
          })
        });
      } else {
        reply(request, handler, 500, {});
      }
    }));
    engine = RealEngine();
    container = ProviderContainer(overrides: [
      sessionStoreProvider.overrideWithValue(store),
      apiClientProvider.overrideWithValue(client),
      vpnEngineProvider.overrideWithValue(engine),
      connectionPreferencesStoreProvider.overrideWithValue(MemoryPreferences()),
      nodesProvider.overrideWith((ref) async => nodes),
      if (me != null) meProvider.overrideWith((ref) async => me),
    ]);
    container.read(selectedNodeProvider.notifier).state = selected;
    await container.read(sessionProvider.future);
    if (me != null) await container.read(meProvider.future);
    return container;
  }

  tearDown(() {
    debugDefaultTargetPlatformOverride = null;
    container.dispose();
    engine.dispose();
  });

  test('只有出口时自动连接调用出口接口', () async {
    await start(nodes: [
      demoNode('e12', protocol: 'VLESS'),
    ]);
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(paths, ['/v1/connect/egress']);
    expect(engine.profiles.single, isA<EgressProfile>());
    expect(engine.profiles.single.nodeId, 'e12');
    expect(container.read(connectionFailureProvider), isNull);
  });

  test('没有套餐时自动连接说明要购买或兑换', () async {
    await start(nodes: const [], me: {
      'entitlement': {'blocked_reason': 'NO_PLAN'}
    });
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(paths, isEmpty);
    expect(container.read(connectionFailureProvider)?.code, 'NO_PLAN');
  });

  test('有套餐但没有线路时不是套餐错误', () async {
    await start(nodes: const [], me: {'entitlement': <String, dynamic>{}});
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(container.read(connectionFailureProvider)?.code, 'NO_LINE');
  });
}
