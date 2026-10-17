import 'dart:async';
import 'dart:convert';

import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/providers.dart';
import 'package:yuhai_vpn/core/network/api_client.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/features/connection/data/connection_api.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_controller.dart';
import 'package:yuhai_vpn/features/settings/data/connection_preferences.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/wireguard_profile.dart';

import 'session_lifecycle_test.dart' show TestDeviceKey, login, reply;
import 'connection_guard_test.dart' show demoNode;
import 'wireguard_profile_test.dart' show clientPublic, profileResponse;

class RealEngine extends MockVpnEngine implements WireGuardVpnEngine {
  final profiles = <ConnectProfile>[];
  final identities = <String>[];
  Completer<String>? pendingKey;
  Completer<bool>? permission;
  int disconnects = 0;
  @override
  Future<String> publicKey(String identity) async {
    identities.add(identity);
    return pendingKey == null ? clientPublic : await pendingKey!.future;
  }

  @override
  Future<bool> prepare() async =>
      permission == null ? true : await permission!.future;
  @override
  Future<void> connect(ConnectProfile profile) async {
    profiles.add(profile);
  }

  @override
  Future<void> disconnect() async {
    disconnects++;
  }
}

class MemoryPreferences implements ConnectionPreferencesStorage {
  ProtocolPreference protocol = ProtocolPreference.auto;
  @override
  Future<ConnectionPreferences> load() async =>
      ConnectionPreferences(protocol: protocol);
  @override
  Future<void> save(ConnectionPreferences value) async {
    protocol = value.protocol;
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  late SessionStore store;
  late ApiClient client;
  late RealEngine engine;
  late ProviderContainer container;
  late List<RequestOptions> requests;
  late MemoryPreferences preferences;
  setUp(() async {
    FlutterSecureStorage.setMockInitialValues({});
    store = SessionStore();
    await store.saveSession(login('123'));
    await store.saveDeviceUid('device-uid');
    client = ApiClient(
        baseUrl: 'http://localhost',
        deviceKey: TestDeviceKey(),
        store: store,
        appVersion: 'test',
        platform: 'ANDROID');
    requests = [];
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      requests.add(request);
      if (request.path == '/v1/devices/current/wg-key') {
        reply(request, handler, 204, {});
      } else if (request.path == '/v1/connect/profiles') {
        reply(request, handler, 200,
            {'data': profileResponse(now: DateTime.now().toUtc())});
      } else {
        reply(request, handler, 500, {});
      }
    }));
    engine = RealEngine();
    preferences = MemoryPreferences();
    container = ProviderContainer(overrides: [
      sessionStoreProvider.overrideWithValue(store),
      apiClientProvider.overrideWithValue(client),
      vpnEngineProvider.overrideWithValue(engine),
      connectionPreferencesStoreProvider.overrideWithValue(preferences),
      nodesProvider
          .overrideWith((ref) async => [demoNode('1', protocol: 'WIREGUARD')]),
    ]);
    container.read(selectedNodeProvider.notifier).state = '1';
    await container.read(sessionProvider.future);
  });
  tearDown(() {
    container.dispose();
    engine.dispose();
    client.dio.close();
  });
  Future<void> waitFor(bool Function() condition) async {
    for (var i = 0; i < 100 && !condition(); i++) {
      await Future<void>.delayed(Duration.zero);
    }
    expect(condition(), isTrue);
  }

  test('真实连接顺序为原生公钥、签名幂等上传、本人配置、原生握手', () async {
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(engine.identities, ['8302571644:123']);
    expect(requests.map((r) => r.path),
        ['/v1/devices/current/wg-key', '/v1/connect/profiles']);
    for (final request in requests) {
      expect(request.headers['X-Signature'], isNotEmpty);
      expect(request.headers['Authorization'], 'Bearer access-123');
      expect(request.headers['X-Device-Id'], 'device-uid');
    }
    expect(requests[0].headers['Idempotency-Key'],
        matches(RegExp(r'^[a-f0-9-]{36}$')));
    expect(
        jsonDecode(utf8.decode(requests[0].data)), {'wg_pubkey': clientPublic});
    expect(jsonDecode(utf8.decode(requests[1].data)), {
      'node_ids': ['1'],
      'protocols': ['WIREGUARD']
    });
    expect(engine.profiles.single, isA<WireGuardProfile>());
    expect((engine.profiles.single as WireGuardProfile).identity,
        '8302571644:123');
    // No native handshake emitted: configuration acceptance must stay connecting.
    expect(container.read(connectionControllerProvider), VpnStatus.connecting);
    await container.read(connectionControllerProvider.notifier).toggle();
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(requests[2].headers['Idempotency-Key'],
        requests[0].headers['Idempotency-Key']);
  });

  test('公钥返回前取消、改线路或退出，都不会上传公钥或连接', () async {
    for (final change in ['cancel', 'selection', 'logout']) {
      engine.pendingKey = Completer<String>();
      engine.identities.clear();
      if (change == 'logout') {
        await store.saveSession(login('123'));
        container.invalidate(sessionProvider);
        await container.read(sessionProvider.future);
      }
      container.read(selectedNodeProvider.notifier).state = '1';
      final controller = container.read(connectionControllerProvider.notifier);
      final pending = controller.toggle();
      await waitFor(() => engine.identities.isNotEmpty);
      switch (change) {
        case 'cancel':
          await controller.toggle();
        case 'selection':
          container.read(selectedNodeProvider.notifier).state = '2';
        case 'logout':
          await container.read(sessionProvider.notifier).signOut();
      }
      engine.pendingKey!.complete(clientPublic);
      await pending;
      expect(requests, isEmpty);
      expect(engine.profiles, isEmpty);
    }
  });

  test('材料返回途中换账户不能连接，旧账户公钥不会上传到新账户', () async {
    final api = ConnectionApi(client);
    final oldSession = login('123');
    await store.saveSession(login('456', account: '9999999999'));
    await expectLater(api.uploadPublicKey(oldSession, clientPublic),
        throwsA(isA<ApiException>()));
    expect(requests, isEmpty);
  });

  test('配置响应期间取消连接，迟到材料不能启动隧道', () async {
    final response = Completer<void>();
    client.dio.interceptors.clear();
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) async {
      requests.add(request);
      if (request.path == '/v1/connect/profiles') {
        await response.future;
        reply(request, handler, 200,
            {'data': profileResponse(now: DateTime.now().toUtc())});
      } else {
        reply(request, handler, 204, {});
      }
    }));
    final controller = container.read(connectionControllerProvider.notifier);
    final pending = controller.toggle();
    await waitFor(() => requests.length == 2);
    await controller.toggle();
    response.complete();
    await pending;
    expect(engine.profiles, isEmpty);
    expect(
        container.read(connectionControllerProvider), VpnStatus.disconnected);
  });

  test('套餐错误准确报告且不进入原生连接', () async {
    client.dio.interceptors.clear();
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      if (request.path == '/v1/connect/profiles') {
        reply(request, handler, 403, {
          'error': {'code': 40303, 'message': 'quota'}
        });
      } else {
        reply(request, handler, 204, {});
      }
    }));
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(engine.profiles, isEmpty);
    expect(container.read(connectionFailureProvider)!.code, 'QUOTA_EXHAUSTED');
  });

  test('用户明确选用尚未实现的协议不会偷偷改为 WireGuard', () async {
    preferences.protocol = ProtocolPreference.trojan;
    await container.read(connectionControllerProvider.notifier).toggle();
    expect(requests, isEmpty);
    expect(engine.profiles, isEmpty);
    expect(container.read(connectionFailureProvider)!.code,
        'PROTOCOL_UNSUPPORTED');
  });
}
