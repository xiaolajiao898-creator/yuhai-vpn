import 'dart:async';
import 'dart:convert';
import 'dart:typed_data';

import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:package_info_plus/package_info_plus.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:yuhai_vpn/app/providers.dart';
import 'package:yuhai_vpn/core/network/api_client.dart';
import 'package:yuhai_vpn/core/security/device_key.dart';
import 'package:yuhai_vpn/core/storage/durable_id_store.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/onboarding/data/auth_api.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_controller.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';

class TestDeviceKey implements DeviceKey {
  int generations = 0;
  @override
  Future<String> publicKeySpkiBase64() async => 'test-public-key-$generations';
  @override
  Future<void> regenerate() async {
    generations++;
  }

  @override
  Future<Uint8List> signDigest(Uint8List digest) async =>
      Uint8List.fromList([1, 2, 3]);
}

class TrackingEngine extends MockVpnEngine {
  int disconnects = 0;
  bool fail = false;
  @override
  Future<void> disconnect() async {
    disconnects++;
    if (fail) throw StateError('engine unavailable');
    await super.disconnect();
  }
}

Session login(String device, {String account = '8302571644'}) => Session(
      accountNo: account,
      deviceId: device,
      accessToken: 'access-$device',
      refreshToken: 'refresh-$device',
      inviteCode: '',
    );

ApiClient clientFor(SessionStore store, TestDeviceKey key) => ApiClient(
      baseUrl: 'http://localhost',
      deviceKey: key,
      store: store,
      appVersion: '1.0.0+100',
      platform: 'ANDROID',
    );

Map<String, dynamic> restored(String id) => {
      'account_no': '8302571644',
      'device_id': id,
      'tokens': {'access_token': 'access-$id', 'refresh_token': 'refresh-$id'},
    };

void reply(RequestOptions request, RequestInterceptorHandler handler,
        int status, Map<String, dynamic> data) =>
    handler.resolve(
        Response(requestOptions: request, statusCode: status, data: data));

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  setUp(() {
    FlutterSecureStorage.setMockInitialValues({});
    SharedPreferences.setMockInitialValues({});
    PackageInfo.setMockInitialValues(
      appName: 'test',
      packageName: 'com.yuhai.test',
      version: '1.0.0',
      buildNumber: '100',
      buildSignature: '',
    );
  });

  test('恢复沿用本机设备身份，设备替换不再换钥匙', () async {
    final store = SessionStore();
    await store.saveDeviceUid('previous-device-uid');
    final key = TestDeviceKey();
    final client = clientFor(store, key);
    final auth = AuthApi(client, key, store);
    Map<String, dynamic>? restoreBody;
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      final body = jsonDecode(utf8.decode(request.data as List<int>))
          as Map<String, dynamic>;
      if (request.path == '/v1/auth/restore') {
        restoreBody = body;
        reply(request, handler, 409, {
          'error': {
            'code': 40902,
            'message': 'full',
            'details': {
              'restore_ticket': 'test-ticket',
              'device_limit': 3,
              'devices': [
                {'id': '9', 'display_name': '旧手机', 'platform': 'ANDROID'}
              ]
            }
          }
        });
      } else {
        expect(body['device_uid'], restoreBody!['device_uid']);
        expect(body['device_pubkey'], restoreBody!['device_pubkey']);
        reply(request, handler, 200, {'data': restored('next-device')});
      }
    }));
    await expectLater(auth.restore('YH-test'), throwsA(isA<DeviceLimitReached>()
        .having((e) => e.limit, 'limit', 3)
        .having((e) => e.ticket, 'ticket', 'test-ticket')
        .having((e) => e.message, 'message', contains('最多 3 台'))));
    expect(restoreBody!['device_uid'], 'previous-device-uid');
    expect(key.generations, 0);
    await auth.replaceDevice('test-ticket', 'old-device');
    expect((await store.session())!.deviceId, 'next-device');
    expect(await store.deviceUid(), 'previous-device-uid');
    expect(key.generations, 0);
  });

  test('密码登录沿用设备身份，40901 只轮换一次', () async {
    final store = SessionStore();
    await store.saveDeviceUid('previous-device-uid');
    final key = TestDeviceKey();
    final client = clientFor(store, key);
    final auth = AuthApi(client, key, store);
    var logins = 0;
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      final body = jsonDecode(utf8.decode(request.data as List<int>))
          as Map<String, dynamic>;
      logins++;
      if (logins == 1) {
        expect(body['device_uid'], 'previous-device-uid');
        expect(body['device_pubkey'], 'test-public-key-0');
        reply(request, handler, 409, {
          'error': {'code': 40901, 'message': '设备标识已存在，请重新生成'}
        });
      } else {
        expect(body['device_uid'], isNot('previous-device-uid'));
        expect(body['device_pubkey'], 'test-public-key-1');
        reply(request, handler, 200, {'data': restored('kept')});
      }
    }));
    await auth.loginWithPassword('TAddress', 'secret');
    expect(logins, 2);
    expect(key.generations, 1);
    expect(await store.deviceUid(), isNot('previous-device-uid'));
    expect((await store.session())!.deviceId, 'kept');
  });

  test('设备标识在安全存储被清空后仍从普通偏好读回', () async {
    final ids = MemoryDurableIdStore();
    final first = SessionStore(const FlutterSecureStorage(), ids);
    await first.saveDeviceUid('phone-uid');
    FlutterSecureStorage.setMockInitialValues(const {});
    final second = SessionStore(const FlutterSecureStorage(), ids);
    expect(await second.deviceUid(), 'phone-uid');
  });

  test('只有安全存储里还有设备标识时会回填到普通偏好', () async {
    final ids = MemoryDurableIdStore();
    FlutterSecureStorage.setMockInitialValues({'device_uid': 'secure-uid'});
    final store = SessionStore(const FlutterSecureStorage(), ids);
    expect(await store.deviceUid(), 'secure-uid');
    expect(ids.values['device_uid'], 'secure-uid');
  });

  test('设备已满但列表解析失败时仍给出上限提示', () {
    final parsed = deviceLimitFrom(ApiException(40902, '设备数量已达上限',
        details: <String, dynamic>{
          'device_limit': 3,
          'restore_ticket': 'ticket',
          'devices': 'not-a-list',
        }));
    expect(parsed, isNotNull);
    expect(parsed!.devices, isEmpty);
    expect(parsed.limit, 3);
    expect(parsed.message, contains('最多 3 台'));
    expect(deviceLimitFrom(ApiException(500, 'no')), isNull);
  });

  test('已登录时恢复不会破坏当前密钥、身份或会话', () async {
    final store = SessionStore();
    final original = login('valid');
    await store.saveSession(original);
    await store.saveDeviceUid('valid-uid');
    final key = TestDeviceKey();
    final auth = AuthApi(clientFor(store, key), key, store);
    await expectLater(auth.restore('YH-test'), throwsA(isA<ApiException>()));
    await expectLater(
        auth.replaceDevice('ticket', 'other'), throwsA(isA<ApiException>()));
    expect(key.generations, 0);
    expect(await store.deviceUid(), 'valid-uid');
    expect((await store.session())!.accessToken, original.accessToken);
  });

  test('并发恢复拒绝第二次，避免请求中途重新生成设备密钥', () async {
    final store = SessionStore();
    final key = TestDeviceKey();
    final client = clientFor(store, key);
    final auth = AuthApi(client, key, store);
    final pending = Completer<(RequestOptions, RequestInterceptorHandler)>();
    client.dio.interceptors.add(InterceptorsWrapper(
        onRequest: (request, handler) => pending.complete((request, handler))));
    final first = auth.restore('YH-test');
    final (request, handler) = await pending.future;
    await expectLater(auth.restore('YH-test'), throwsA(isA<ApiException>()));
    expect(key.generations, 0);
    reply(request, handler, 200, {'data': restored('new')});
    await first;
  });

  test('40107 清理路由会话并断开连接，销毁监听后不留下回调', () async {
    final store = SessionStore();
    await store.saveSession(login('old'));
    final key = TestDeviceKey();
    final client = clientFor(store, key);
    final engine = TrackingEngine()..fail = true;
    final container = ProviderContainer(overrides: [
      sessionStoreProvider.overrideWithValue(store),
      apiClientProvider.overrideWithValue(client),
      vpnEngineProvider.overrideWithValue(engine),
    ]);
    await container.read(sessionProvider.future);
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      reply(request, handler, 401, {
        'error': {'code': 40107, 'message': 'revoked'}
      });
    }));
    await expectLater(
        client.call('GET', '/v1/me'), throwsA(isA<ApiException>()));
    await Future<void>.delayed(Duration.zero);
    expect(await store.session(), isNull);
    expect(container.read(sessionProvider).valueOrNull, isNull);
    expect(engine.disconnects, 1);
    container.dispose();
    expect(client.onSessionLost, isNull);
  });

  test('旧请求迟到的401不会清除新登录或触发失效回调', () async {
    final store = SessionStore();
    await store.saveSession(login('old'));
    final client = clientFor(store, TestDeviceKey());
    int lost = 0;
    client.onSessionLost = (_) => lost++;
    final pending = Completer<(RequestOptions, RequestInterceptorHandler)>();
    client.dio.interceptors.add(InterceptorsWrapper(
        onRequest: (request, handler) => pending.complete((request, handler))));
    final requestFuture = client.call('GET', '/v1/me');
    final (request, handler) = await pending.future;
    await store.saveSession(login('new', account: '1234567890'));
    reply(request, handler, 401, {
      'error': {'code': 40107, 'message': 'revoked'}
    });
    await expectLater(requestFuture, throwsA(isA<ApiException>()));
    expect((await store.session())!.deviceId, 'new');
    expect(lost, 0);
  });

  test('退出后迟到的刷新结果不会复活已注销会话', () async {
    final store = SessionStore();
    await store.saveSession(login('old'));
    await store.saveDeviceUid('old-uid');
    final client = clientFor(store, TestDeviceKey());
    final pending = Completer<(RequestOptions, RequestInterceptorHandler)>();
    client.dio.interceptors.add(InterceptorsWrapper(
        onRequest: (request, handler) => pending.complete((request, handler))));
    final refresh = client.refresh();
    final (request, handler) = await pending.future;
    await store.clearSession();
    reply(request, handler, 200, {
      'data': {'access_token': 'late-access', 'refresh_token': 'late-refresh'}
    });
    await refresh;
    expect(await store.session(), isNull);
  });

  test('同时刷新共用一个请求，不重放一次性刷新令牌', () async {
    final store = SessionStore();
    await store.saveSession(login('old'));
    await store.saveDeviceUid('old-uid');
    final client = clientFor(store, TestDeviceKey());
    int calls = 0;
    final pending = Completer<(RequestOptions, RequestInterceptorHandler)>();
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      calls++;
      pending.complete((request, handler));
    }));
    final first = client.refresh();
    final (request, handler) = await pending.future;
    final second = client.refresh();
    reply(request, handler, 200, {
      'data': {'access_token': 'next-access', 'refresh_token': 'next-refresh'}
    });
    await Future.wait([first, second]);
    expect(calls, 1);
    expect((await store.session())!.refreshToken, 'next-refresh');
  });

  for (final code in [40101, 40105]) {
    test('旧账户请求返回$code时不能重试为新登录', () async {
      final store = SessionStore();
      await store.saveSession(login('old'));
      await store.saveDeviceUid('old-uid');
      final client = clientFor(store, TestDeviceKey());
      final pending = Completer<(RequestOptions, RequestInterceptorHandler)>();
      int requests = 0;
      client.dio.interceptors
          .add(InterceptorsWrapper(onRequest: (request, handler) {
        requests++;
        if (requests == 1) {
          pending.complete((request, handler));
        } else {
          reply(request, handler, 200, {'data': restored('unexpected')});
        }
      }));
      final operation = client.call('POST', '/v1/orders',
          json: {'plan_code': 'monthly'}, sign: true);
      final (request, handler) = await pending.future;
      await store.saveSession(login('new', account: '1234567890'));
      reply(request, handler, 401, {
        'error': {
          'code': code,
          'message': 'expired',
          'details': {'server_time': '2026-09-12T00:00:00Z'}
        }
      });
      await expectLater(operation, throwsA(isA<ApiException>()));
      expect(requests, 1);
      expect((await store.session())!.deviceId, 'new');
      expect(client.clockOffsetSeconds, 0);
    });
  }

  for (final conditional in [false, true]) {
    test('${conditional ? '条件GET' : '写请求'}刷新途中更换登录不会重放', () async {
      final store = SessionStore();
      await store.saveSession(login('old'));
      await store.saveDeviceUid('old-uid');
      final client = clientFor(store, TestDeviceKey());
      final pendingRefresh =
          Completer<(RequestOptions, RequestInterceptorHandler)>();
      final paths = <String>[];
      client.dio.interceptors
          .add(InterceptorsWrapper(onRequest: (request, handler) {
        paths.add(request.path);
        if (request.path == '/v1/auth/refresh') {
          pendingRefresh.complete((request, handler));
        } else {
          reply(request, handler, 401, {
            'error': {'code': 40101, 'message': 'expired'}
          });
        }
      }));
      final Future<dynamic> operation = conditional
          ? client.getIfChanged('/v1/orders/old-order', etag: 'old-etag')
          : client.call('POST', '/v1/orders',
              json: {'plan_code': 'monthly'}, sign: true);
      final (request, handler) = await pendingRefresh.future;
      // Even the same account restoring on a new device is a new login.
      await store.saveSession(login('new'));
      reply(request, handler, 200, {
        'data': {'access_token': 'late-access', 'refresh_token': 'late-refresh'}
      });
      await expectLater(operation, throwsA(isA<ApiException>()));
      expect(paths, [
        conditional ? '/v1/orders/old-order' : '/v1/orders',
        '/v1/auth/refresh'
      ]);
      expect((await store.session())!.deviceId, 'new');
      expect((await store.session())!.accessToken, 'access-new');
    });
  }

  test('条件GET最多刷新重试一次，持续401不会无限递归', () async {
    final store = SessionStore();
    await store.saveSession(login('old'));
    await store.saveDeviceUid('old-uid');
    final client = clientFor(store, TestDeviceKey());
    int reads = 0, refreshes = 0;
    client.dio.interceptors
        .add(InterceptorsWrapper(onRequest: (request, handler) {
      if (request.path == '/v1/auth/refresh') {
        refreshes++;
        reply(request, handler, 200, {
          'data': {
            'access_token': 'next-access',
            'refresh_token': 'next-refresh'
          }
        });
      } else {
        reads++;
        expect(request.headers['If-None-Match'], 'order-etag');
        if (reads > 2) {
          handler.reject(DioException(
              requestOptions: request, error: 'unexpected third read'));
          return;
        }
        reply(request, handler, 401, {
          'error': {'code': 40101, 'message': 'expired'}
        });
      }
    }));
    await expectLater(
        client.getIfChanged('/v1/orders/test', etag: 'order-etag'),
        throwsA(isA<ApiException>()));
    expect(reads, 2);
    expect(refreshes, 1);
  });
}
