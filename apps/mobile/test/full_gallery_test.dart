import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/desktop_frame.dart';
import 'package:yuhai_vpn/app/providers.dart';
import 'package:yuhai_vpn/app/router.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/core/widgets/tide_ring_button.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/nodes/data/node_shortcuts.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/backup_page.dart';
import 'package:yuhai_vpn/features/settings/data/connection_preferences.dart';
import 'package:yuhai_vpn/features/settings/data/device_removal_auth.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';
import 'package:yuhai_vpn/preview/preview_api.dart';

import 'screenshot_fonts.dart';

/// 全部页面的渲染图（改版前后对比用）。只在本地设了 YH_SCREENSHOTS=1、带 --update-goldens 时写出 PNG；
/// CI 不设，这里直接返回。数据来自前端演示的假接口（preview_api.dart），套餐和账户按真实产品改成
/// 月卡 6 / 季卡 16 / 年卡 50 USDT、3 台设备、不限流量。不连网络，不建真实连接。
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('full gallery · phone', (tester) async {
    if (Platform.environment['YH_SCREENSHOTS'] != '1') return;
    await _prepare(tester);
    _window(tester, const Size(360, 800), 3);
    await _shootAll(tester, 'phone', desktop: false);
    await _teardown(tester);
  });

  testWidgets('full gallery · desktop', (tester) async {
    if (Platform.environment['YH_SCREENSHOTS'] != '1') return;
    await _prepare(tester);
    _window(tester, const Size(1920, 1000), 1);
    await _shootDesktop(tester, 'desktop-max');
    _window(tester, const Size(424, 820), 2);
    await _shootDesktop(tester, 'desktop-default');
    await _teardown(tester);
  }, variant: TargetPlatformVariant.only(TargetPlatform.windows));
}

Future<void> _prepare(WidgetTester tester) async {
  await tester.runAsync(loadScreenshotFonts);
  // 改版后新增的字体；改版前的代码没有这些资源，加载失败就跳过。
  await tester.runAsync(() async {
    for (final (family, assets) in [
      ('YhDisplay', ['assets/fonts/SmileySans-Oblique.otf']),
      ('YhLatin', ['assets/fonts/Unbounded-Medium.ttf', 'assets/fonts/Unbounded-Bold.ttf']),
    ]) {
      try {
        final loader = FontLoader(family);
        for (final a in assets) {
          loader.addFont(rootBundle.load(a));
        }
        await loader.load();
      } catch (_) {}
    }
    // 国旗 emoji：测试环境没有系统字体回退。Mac 上借用系统自带的彩色 emoji 字体，
    // 注册成回退列表最后一项 'sans-serif'。找不到或加载失败就跳过，只影响截图里的国旗。
    for (final path in const ['/System/Library/Fonts/Apple Color Emoji.ttc']) {
      try {
        final file = File(path);
        if (!file.existsSync()) continue;
        final bytes = await file.readAsBytes();
        final loader = FontLoader('sans-serif')..addFont(Future.value(ByteData.sublistView(bytes)));
        await loader.load();
        break;
      } catch (_) {}
    }
  });
  FlutterSecureStorage.setMockInitialValues({});
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

void _window(WidgetTester tester, Size logical, double ratio) {
  tester.view.devicePixelRatio = ratio;
  tester.view.physicalSize = logical * ratio;
}

/// 有周期计时器或循环动画的页面不能 pumpAndSettle，这里固定推进一段时间。
Future<void> _settle(WidgetTester tester, {int steps = 8}) async {
  for (var i = 0; i < steps; i++) {
    await tester.pump(const Duration(milliseconds: 150));
  }
}

Future<void> _shoot(WidgetTester tester, String dir, String name) async {
  await _settle(tester);
  // 图片（吉祥物艾丽）在测试里要真正解码完才画得出来。
  await tester.runAsync(() async {
    for (final element in find.byType(Image).evaluate()) {
      final image = element.widget as Image;
      try {
        await precacheImage(image.image, element);
      } catch (_) {}
    }
  });
  await tester.pump();
  final error = tester.takeException();
  if (error != null) debugPrint('gallery $dir/$name: $error');
  await expectLater(find.byType(MaterialApp), matchesGoldenFile('goldens/full/$dir/$name.png'));
}

final _containers = <ProviderContainer>[];

/// 先卸下页面，再关掉所有 ProviderContainer：演示引擎的计时器随之停止，测试结束时没有挂着的计时器。
Future<void> _teardown(WidgetTester tester) async {
  await tester.pumpWidget(const SizedBox());
  for (final c in _containers) {
    c.dispose();
  }
  _containers.clear();
  await tester.pump(const Duration(seconds: 1));
}

class _App {
  _App(this.container);
  final ProviderContainer container;
  void go(String location, {Object? extra}) => container.read(routerProvider).go(location, extra: extra);
}

Future<_App> _pumpApp(WidgetTester tester, {required bool loggedIn, bool noPlan = false, bool desktop = false}) async {
  final store = PreviewSessionStore();
  if (!loggedIn) await store.clearSession();
  final api = _GalleryApi(store, noPlan: noPlan);
  final container = ProviderContainer(overrides: [
    nodeDemoModeProvider.overrideWithValue(true),
    vpnDemoModeProvider.overrideWithValue(true),
    vpnEngineProvider.overrideWith((ref) {
      final engine = MockVpnEngine();
      ref.onDispose(engine.dispose);
      return engine;
    }),
    sessionStoreProvider.overrideWithValue(store),
    apiClientProvider.overrideWithValue(api),
    deviceKeyProvider.overrideWithValue(PreviewDeviceKey()),
    connectionPreferencesStoreProvider.overrideWithValue(PreviewPreferencesStorage()),
    nodeShortcutsStoreProvider.overrideWithValue(MemoryNodeShortcutsStorage()),
    deviceRemovalAuthProvider.overrideWithValue(_Auth()),
  ]);
  _containers.add(container);
  await tester.pumpWidget(UncontrolledProviderScope(
    key: UniqueKey(),
    container: container,
    child: Consumer(
      builder: (context, ref, _) => MaterialApp.router(
        title: '宇海VPN',
        theme: buildDeepSeaTheme(),
        debugShowCheckedModeBanner: false,
        routerConfig: ref.watch(routerProvider),
        builder: (context, child) =>
            DesktopFrame(enabled: desktop, child: child ?? const SizedBox.shrink()),
      ),
    ),
  ));
  await _settle(tester, steps: 6);
  return _App(container);
}

Future<void> _shootAll(WidgetTester tester, String dir, {required bool desktop}) async {
  // 启动页
  await tester.pumpWidget(MaterialApp(
      key: UniqueKey(), theme: buildDeepSeaTheme(), debugShowCheckedModeBanner: false, home: const YuhaiSplash()));
  await _shoot(tester, dir, '01-splash');

  // 未登录：引导、注册、登录、找回、恢复
  var app = await _pumpApp(tester, loggedIn: false, desktop: desktop);
  await _shoot(tester, dir, '02-onboarding-1');
  for (var i = 2; i <= 3; i++) {
    final next = find.text('继续');
    if (next.evaluate().isNotEmpty) {
      await tester.tap(next.first);
    } else {
      await tester.drag(find.byType(PageView), const Offset(-300, 0));
    }
    await _settle(tester);
    await _shoot(tester, dir, '0${i + 1}-onboarding-$i');
  }
  app.go(Routes.register);
  await _shoot(tester, dir, '05-register');
  app.go(Routes.login);
  await _shoot(tester, dir, '06-login');
  app.go(Routes.resetPassword);
  await _shoot(tester, dir, '07-reset-password');
  app.go(Routes.restore);
  await _shoot(tester, dir, '08-restore');
  app.go(Routes.backup,
      extra: const BackupArgs(accountNo: 'YH888876', accountSecret: 'YH-7K2M-9QXR-4TPA-HB3D-W8NF-6CJE'));
  await _shoot(tester, dir, '09-backup');

  // 新账户：没有套餐，首页显示试用
  app = await _pumpApp(tester, loggedIn: true, noPlan: true, desktop: desktop);
  app.go(Routes.home);
  await _shoot(tester, dir, '10-home-no-plan');

  // 有套餐的账户
  app = await _pumpApp(tester, loggedIn: true, desktop: desktop);
  app.go(Routes.home);
  await _shoot(tester, dir, '11-home');
  final ring = find.byType(TideRingButton);
  if (ring.evaluate().isNotEmpty) {
    await tester.tap(ring.first);
    await _settle(tester, steps: 30);
    await _shoot(tester, dir, '12-home-connected');
    app.go(Routes.home);
  }
  for (final (name, route, extra) in <(String, String, Object?)>[
    ('13-nodes', Routes.nodes, null),
    ('14-plans', Routes.plans, null),
    ('15-order-confirm', Routes.orderConfirm, 'QUARTER'),
    ('16-pay', Routes.pay.replaceFirst(':orderNo', 'YH20261110000016'), _pendingOrder),
    ('17-pay-result', Routes.payResult.replaceFirst(':orderNo', 'YH20261110000016'), null),
    ('18-orders', Routes.orders, null),
    ('19-invite', Routes.invite, null),
    ('20-rewards', Routes.rewards, null),
    ('21-settings', Routes.settings, null),
    ('22-account', Routes.account, null),
    ('23-redeem', Routes.redeem, null),
    ('24-devices', Routes.devices, null),
    ('25-connection', Routes.connection, null),
    ('26-balance', Routes.balance, null),
    ('27-traffic', Routes.traffic, null),
    ('28-about', Routes.about, null),
  ]) {
    app.go(route, extra: extra);
    await _shoot(tester, dir, name);
  }
  app.go(Routes.home);
  await _settle(tester);
}

Future<void> _shootDesktop(WidgetTester tester, String dir) async {
  var app = await _pumpApp(tester, loggedIn: false, desktop: true);
  await _shoot(tester, dir, '01-onboarding');
  app.go(Routes.login);
  await _shoot(tester, dir, '02-login');
  app.go(Routes.register);
  await _shoot(tester, dir, '03-register');
  app = await _pumpApp(tester, loggedIn: true, desktop: true);
  app.go(Routes.home);
  await _shoot(tester, dir, '04-home');
  app.go(Routes.plans);
  await _shoot(tester, dir, '05-plans');
  app.go(Routes.settings);
  await _shoot(tester, dir, '06-settings');
  app.go(Routes.home);
  await _settle(tester);
}

final Map<String, dynamic> _pendingOrder = () {
  final now = DateTime.now().toUtc();
  return <String, dynamic>{
    'order_no': 'YH20261110000016',
    'order_type': 'PLAN',
    'status': 'PENDING',
    'pay_method': 'USDT_TRC20',
    'plan_name': '季卡',
    'base_amount': '16.000000',
    'pay_amount': '16.000317',
    'network': 'TRON',
    'receive_address': 'TKbBqAMUdGxNseYr92FYesZLiy9D9S5idf',
    'expires_at': now.add(const Duration(minutes: 14, seconds: 32)).toIso8601String(),
    'created_at': now.toIso8601String(),
    'server_time': now.toIso8601String(),
  };
}();

class _Auth extends DeviceRemovalAuthenticator {
  @override
  Future<bool> confirmIdentity() async => true;
}

/// 演示接口，按真实产品改了账户和套餐，补上试用状态和待付款订单。
class _GalleryApi extends PreviewApi {
  _GalleryApi(super.store, {required this.noPlan});
  final bool noPlan;

  @override
  Future<Map<String, dynamic>> call(String method, String path,
      {Map<String, dynamic>? json,
      Map<String, List<String>> query = const {},
      bool sign = false,
      String? idempotencyKey,
      bool retrying = false,
      Session? expectedSession,
      Duration? timeout}) async {
    final now = DateTime.now().toUtc();
    String at(int days) => now.add(Duration(days: days)).toIso8601String();
    if (method == 'GET' && path == '/v1/me') {
      return {
        'account_no': 'YH888876',
        'status': 'ACTIVE',
        'backup_acknowledged': true,
        'balance': '0.000000',
        'pending_order': null,
        'security_notices': [],
        'announcements_unread': 0,
        'server_time': at(0),
        'entitlement': noPlan
            ? {'can_connect': false, 'blocked_reason': 'NO_PLAN', 'tier': 0, 'device_count': 1, 'device_limit': 3}
            : {
                'can_connect': true,
                'blocked_reason': null,
                'tier': 1,
                'plan_name': '季卡',
                'expire_at': at(63),
                'device_count': 2,
                'device_limit': 3,
                'period_quota_bytes': null,
                'traffic_packs': [],
              },
      };
    }
    if (method == 'GET' && path == '/v1/trial') {
      return noPlan
          ? {'enabled': true, 'eligible': true, 'status': 'available', 'tier': 1, 'device_limit': 3, 'unlimited_traffic': true, 'duration_hours': 24}
          : {'enabled': true, 'eligible': false, 'status': 'used', 'reason': 'ALREADY_USED', 'tier': 1, 'device_limit': 3, 'unlimited_traffic': true, 'duration_hours': 24};
    }
    if (method == 'GET' && path == '/v1/plans') {
      return {
        'plans': [
          for (final (code, name, price, days) in [
            ('MONTH', '月卡', '6', 30),
            ('QUARTER', '季卡', '16', 90),
            ('YEAR', '年卡', '50', 365),
          ])
            {'code': code, 'name': name, 'type': 'TIME', 'tier': 1, 'list_price': price, 'duration_days': days, 'device_limit': 3},
        ]
      };
    }
    if (method == 'POST' && path == '/v1/plans/quote') {
      final code = json?['plan_code'];
      final (name, price, days) = switch (code) {
        'MONTH' => ('月卡', '6', 30),
        'YEAR' => ('年卡', '50', 365),
        _ => ('季卡', '16', 90),
      };
      return {
        'quote_id': 'gallery-quote',
        'plan': {'name': name},
        'change_type': 'RENEW',
        'base_amount': price,
        'lines': [
          {'label': '$name · $days 天', 'amount': price}
        ],
        'balance_available': '0.000000',
        'new_expire_at': at(63 + days),
        'expires_at': now.add(const Duration(minutes: 15)).toIso8601String(),
      };
    }
    if (method == 'GET' && path.startsWith('/v1/orders/')) {
      return _pendingOrder;
    }
    return super.call(method, path,
        json: json,
        query: query,
        sign: sign,
        idempotencyKey: idempotencyKey,
        retrying: retrying,
        expectedSession: expectedSession);
  }
}
