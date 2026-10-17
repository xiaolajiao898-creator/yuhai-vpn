import 'package:dio/dio.dart';
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

void main() {
  test('公开版本查询不读取或附带账户令牌', () async {
    final api = client();
    api.dio.interceptors.add(InterceptorsWrapper(onRequest: (request, handler) {
      expect(request.path, '/app/version');
      expect(request.headers.keys.map((e) => e.toLowerCase()),
          isNot(contains('authorization')));
      expect(request.headers.keys.map((e) => e.toLowerCase()),
          isNot(contains('x-device-id')));
      expect(request.queryParameters['current_code'], '100');
      handler.resolve(Response(requestOptions: request, statusCode: 200, data: {
        'data': {
          'platform': 'ANDROID',
          'latest': {
            'version_name': '1.1.0',
            'version_code': 110,
            'notes': '已发布版本'
          },
          'min_supported_code': 100,
          'force_update': false
        }
      }));
    }));
    expect((await AppInfoApi(api).latest()).code, 110);
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
