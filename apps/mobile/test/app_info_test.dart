import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/core/network/api_client.dart';
import 'package:yuhai_vpn/core/security/device_key.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/features/settings/data/app_info_api.dart';
import 'package:yuhai_vpn/features/settings/presentation/about_page.dart';

class ForbiddenSessionStore extends SessionStore {
  @override
  Future<Session?> session() async => throw StateError('版本查询不应读取会话');
}

ApiClient client() => ApiClient(
    baseUrl: 'https://test.invalid',
    deviceKey: NativeDeviceKey(),
    store: ForbiddenSessionStore(),
    appVersion: '1.0.0+100',
    platform: 'ANDROID');

class UnpublishedInfo extends AppInfoApi {
  UnpublishedInfo() : super(client());
  @override
  Future<AppRelease> latest() async => throw ApiException(40401, '没有正式版本');
}

const _releaseBody = {
  'version': '1.1.0',
  'build': 110,
  'url': 'https://yhvpn.io/download/app',
  'sha256': 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  'size': 1024,
  'released_at': '2026-11-13T00:00:00Z',
  'min_supported_build': 100,
  'notes': '已发布版本',
};

void main() {
  test('公开版本查询走 /v1/app/latest 且不读取账户令牌', () async {
    debugDefaultTargetPlatformOverride = TargetPlatform.android;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    final api = client();
    api.dio.interceptors.add(InterceptorsWrapper(onRequest: (request, handler) {
      expect(request.path, '/v1/app/latest');
      expect(request.queryParameters['platform'], 'android');
      expect(request.queryParameters.containsKey('current_code'), isFalse);
      expect(request.headers.keys.map((e) => e.toLowerCase()),
          isNot(contains('authorization')));
      expect(request.headers.keys.map((e) => e.toLowerCase()),
          isNot(contains('x-device-id')));
      handler.resolve(Response(
          requestOptions: request,
          statusCode: 200,
          data: {'data': _releaseBody}));
    }));
    final release = await AppInfoApi(api).latest();
    expect(release.code, 110);
    expect(release.name, '1.1.0');
    expect(release.notes, '已发布版本');
    expect(release.forceUpdate, isFalse);
    expect(release.downloadUrl, 'https://yhvpn.io/download/app');
  });
  test('Windows 检查更新使用 platform=windows', () async {
    debugDefaultTargetPlatformOverride = TargetPlatform.windows;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    final api = client();
    api.dio.interceptors.add(InterceptorsWrapper(onRequest: (request, handler) {
      expect(request.path, '/v1/app/latest');
      expect(request.queryParameters['platform'], 'windows');
      handler.resolve(Response(requestOptions: request, statusCode: 200, data: {
        'data': {..._releaseBody, 'min_supported_build': 200}
      }));
    }));
    final release = await AppInfoApi(api).latest();
    expect(release.forceUpdate, isTrue);
    expect(release.code, 110);
  });
  test('其他系统不请求旧的版本地址', () async {
    debugDefaultTargetPlatformOverride = TargetPlatform.iOS;
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    final api = client();
    var called = false;
    api.dio.interceptors.add(InterceptorsWrapper(onRequest: (request, handler) {
      called = true;
      handler.resolve(
          Response(requestOptions: request, statusCode: 500, data: {}));
    }));
    await expectLater(AppInfoApi(api).latest(), throwsFormatException);
    expect(called, isFalse);
  });
  test('版本与政策链接只接受没有账户凭据的HTTPS地址', () {
    expect(safeReleaseLink('https://download.example/app'), isNotNull);
    for (final url in [
      'http://download.example/app',
      'javascript:alert(1)',
      'https://user:password@example/app',
      'https:///',
      ''
    ]) {
      expect(safeReleaseLink(url), isNull);
    }
  });
  testWidgets('尚未发布时不错误提示当前已是最新版', (tester) async {
    await tester.pumpWidget(ProviderScope(
        overrides: [appInfoApiProvider.overrideWithValue(UnpublishedInfo())],
        child:
            MaterialApp(theme: buildDeepSeaTheme(), home: const AboutPage())));
    await tester.tap(find.text('检查更新'));
    await tester.pumpAndSettle();
    expect(find.text('尚未发布正式版本，暂时没有可用的安装包。'), findsOneWidget);
    expect(find.text('当前已是最新版本'), findsNothing);
    expect(find.text('复制官方安装链接'), findsNothing);
  });
}
