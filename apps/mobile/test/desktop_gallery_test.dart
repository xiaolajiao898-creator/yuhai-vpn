import 'dart:io';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/desktop_frame.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/login_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/register_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/reset_password_page.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/restore_page.dart';

import 'screenshot_fonts.dart';

/// 电脑版界面渲染图：默认窗口和最大化两种大小。和 redesign_gallery_test 一样，
/// 只在本地设了 YH_SCREENSHOTS=1、带 --update-goldens 时写出 PNG；CI 不设，这里直接返回。
///
/// 这是 flutter_test 的渲染结果，界面代码和 Windows / Mac 版相同，不含系统窗口的标题栏和边框。
void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  testWidgets('desktop gallery', (tester) async {
    if (Platform.environment['YH_SCREENSHOTS'] != '1') return;
    await tester.runAsync(loadScreenshotFonts);
    FlutterSecureStorage.setMockInitialValues({});
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    // 默认窗口 440×860 去掉标题栏和边框后约 424×820；最大化按 1080p 屏幕减去任务栏。
    void window(Size logical, double ratio) {
      tester.view.devicePixelRatio = ratio;
      tester.view.physicalSize = logical * ratio;
    }

    Future<void> shoot(String name) async {
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 400));
      await expectLater(
        find.byType(MaterialApp),
        matchesGoldenFile('goldens/desktop/$name.png'),
      );
    }

    // 和 app.dart 一样，在 MaterialApp 的 builder 里套 DesktopFrame。
    Widget frame(Widget home) => ProviderScope(
          key: UniqueKey(),
          child: MaterialApp(
            theme: buildDeepSeaTheme(),
            debugShowCheckedModeBanner: false,
            builder: (context, child) =>
                DesktopFrame(child: child ?? const SizedBox.shrink()),
            home: home,
          ),
        );

    Future<void> onboarding(String prefix) async {
      await tester.pumpWidget(frame(const OnboardingPage()));
      await shoot('$prefix-01-onboarding-1');
      for (var i = 2; i <= 3; i++) {
        await tester.tap(find.text('继续'));
        await tester.pumpAndSettle();
        await shoot('$prefix-0$i-onboarding-$i');
      }
    }

    window(const Size(424, 820), 2);
    await onboarding('default');
    await tester.pumpWidget(frame(const RegisterPage()));
    await shoot('default-04-register');
    await tester.drag(
        find.byType(SingleChildScrollView).first, const Offset(0, -2000));
    await tester.pumpAndSettle();
    await shoot('default-05-register-scrolled');
    await tester.pumpWidget(frame(const LoginPage()));
    await shoot('default-06-login');
    await tester.pumpWidget(frame(const ResetPasswordPage()));
    await shoot('default-07-reset-password');
    await tester.pumpWidget(frame(const RestorePage()));
    await shoot('default-08-restore');

    window(const Size(1920, 1000), 1);
    await tester.pumpWidget(frame(const OnboardingPage()));
    await tester.tap(find.text('继续'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('继续'));
    await tester.pumpAndSettle();
    await shoot('maximized-01-onboarding-3');
    await tester.pumpWidget(frame(const RegisterPage()));
    await shoot('maximized-02-register');
    await tester.pumpWidget(frame(const LoginPage()));
    await shoot('maximized-03-login');
    // 按 Windows 渲染：电脑上按钮等控件用紧凑密度，和 Windows 版一致。
  }, variant: TargetPlatformVariant.only(TargetPlatform.windows));
}
