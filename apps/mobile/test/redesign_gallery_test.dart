import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/router.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/core/widgets/tide_ring_button.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
import 'package:yuhai_vpn/features/home/presentation/home_page.dart';
import 'package:yuhai_vpn/features/nodes/data/node_shortcuts.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/presentation/nodes_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/register_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/reset_password_page.dart';
import 'package:yuhai_vpn/features/plans/data/plans_api.dart';
import 'package:yuhai_vpn/features/plans/presentation/plans_page.dart';
import 'package:yuhai_vpn/features/settings/presentation/connection_settings_page.dart';
import 'package:yuhai_vpn/features/settings/presentation/settings_page.dart';
import 'package:yuhai_vpn/platform/vpn/unavailable_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

/// 只在本地截图时写出 PNG。CI 不设置 YH_SCREENSHOTS，因此不会依赖字体光栅。
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('redesign gallery', (tester) async {
    if (Platform.environment['YH_SCREENSHOTS'] != '1') return;
    FlutterSecureStorage.setMockInitialValues({});
    // 1080×2400 物理像素，3 倍，逻辑宽度 360。和常见安卓手机同一套像素。
    tester.view.physicalSize = const Size(1080, 2400);
    tester.view.devicePixelRatio = 3;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    Future<void> shoot(String name) async {
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 400));
      await expectLater(
        find.byType(MaterialApp),
        matchesGoldenFile('goldens/$name.png'),
      );
    }

    Widget frame(Widget home, {List<Override> overrides = const []}) {
      return ProviderScope(
        key: UniqueKey(),
        overrides: [
          meProvider.overrideWith((ref) async => const {
                'account_no': '10001',
                'backup_acknowledged': true,
                'entitlement': {'blocked_reason': 'NO_PLAN'},
              }),
          nodesProvider.overrideWith((ref) async => const [
                NodeItem(
                  id: '1',
                  name: '洛杉矶 01',
                  countryCode: 'US',
                  city: '洛杉矶',
                  region: '北美',
                  available: true,
                  isRecommended: true,
                  protocols: ['WIREGUARD'],
                  load: 20,
                ),
                NodeItem(
                  id: '2',
                  name: '东京 01',
                  countryCode: 'JP',
                  city: '东京',
                  region: '亚太',
                  available: false,
                  lockedReason: 'TIER',
                  isRecommended: false,
                  protocols: ['WIREGUARD'],
                ),
              ]),
          nodeShortcutsStoreProvider
              .overrideWithValue(MemoryNodeShortcutsStorage()),
          connectionControllerProvider
              .overrideWith(() => _IdleConnection()),
          vpnEngineProvider.overrideWithValue(UnavailableVpnEngine()),
          ...overrides,
        ],
        child: MaterialApp(
          theme: buildDeepSeaTheme(),
          debugShowCheckedModeBanner: false,
          home: home,
        ),
      );
    }

    await tester.pumpWidget(frame(const YuhaiSplash()));
    await shoot('splash');

    await tester.pumpWidget(frame(const OnboardingPage()));
    await shoot('onboarding-1');
    await tester.drag(find.byType(PageView), const Offset(-300, 0));
    await tester.pumpAndSettle();
    await shoot('onboarding-2');
    await tester.drag(find.byType(PageView), const Offset(-300, 0));
    await tester.pumpAndSettle();
    await shoot('onboarding-3');

    await tester.pumpWidget(frame(const RegisterPage()));
    await shoot('register');

    await tester.pumpWidget(frame(const ResetPasswordPage()));
    await shoot('reset-password');

    await tester.pumpWidget(frame(const HomePage()));
    await tester.pumpAndSettle();
    await shoot('home-no-plan');
    await tester.tap(find.byType(TideRingButton));
    await tester.pumpAndSettle();
    await shoot('home-trial-sheet');

    await tester.pumpWidget(frame(
      const PlansPage(),
      overrides: [
        plansProvider.overrideWith((ref) async => const [
              PlanItem(
                code: 'W',
                name: '周卡',
                type: 'TIME',
                tier: 1,
                listPrice: '4.9',
                durationDays: 7,
                deviceLimit: 3,
              ),
              PlanItem(
                code: 'M',
                name: '月卡',
                type: 'TIME',
                tier: 1,
                listPrice: '12',
                durationDays: 30,
                deviceLimit: 3,
                badge: '推荐',
              ),
              PlanItem(
                code: 'Y',
                name: '年卡',
                type: 'TIME',
                tier: 1,
                listPrice: '79',
                durationDays: 365,
                deviceLimit: 3,
              ),
            ]),
      ],
    ));
    await tester.pumpAndSettle();
    await shoot('plans');

    await tester.pumpWidget(frame(
      const PlansPage(),
      overrides: [
        plansProvider.overrideWith((ref) async => <PlanItem>[]),
      ],
    ));
    await tester.pumpAndSettle();
    await shoot('plans-empty');

    await tester.pumpWidget(frame(const NodesPage()));
    await tester.pumpAndSettle();
    await shoot('nodes');

    await tester.pumpWidget(frame(const ConnectionSettingsPage()));
    await tester.pumpAndSettle();
    await shoot('connection-settings');

    await tester.pumpWidget(frame(const SettingsPage()));
    await tester.pumpAndSettle();
    await shoot('settings');
    await tester.ensureVisible(find.text('联系客服'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('联系客服'));
    await tester.pumpAndSettle();
    await shoot('support');
  });
}

class _IdleConnection extends ConnectionController {
  @override
  VpnStatus build() => VpnStatus.disconnected;
}
