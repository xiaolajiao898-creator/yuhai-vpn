import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/brand_scenes.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_page.dart';

/// 引导和注册页的两处排版：插画里的卡片不被框裁掉；最后一屏底部的链接不贴窗口边。
void main() {
  Future<void> setWindow(WidgetTester tester, Size size) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
  }

  // 360 宽的手机、电脑默认窗口（页面约 424 宽），各减去左右 24 的页边距。
  for (final width in [312.0, 376.0]) {
    testWidgets('注册页钱包插画：宽 $width 时两张卡片都完整落在框里', (tester) async {
      await tester.pumpWidget(MaterialApp(
        debugShowCheckedModeBanner: false,
        home: Scaffold(
          body: Center(
            child: SizedBox(
              width: width,
              child:
                  const BrandSceneArt(scene: BrandScene.wallet, compact: true),
            ),
          ),
        ),
      ));
      final art = tester.renderObject<RenderCustomPaint>(find.descendant(
        of: find.byType(BrandSceneArt),
        matching: find.byType(CustomPaint),
      ));
      final size = art.size;
      var shapes = 0;
      expect(
        art,
        paints
          ..everything((method, arguments) {
            if (method != #drawRRect) return true;
            shapes++;
            final rect = (arguments.first as RRect).outerRect;
            if (rect.left < 0 ||
                rect.top < 0 ||
                rect.right > size.width ||
                rect.bottom > size.height) {
              throw '有一块图形超出了插画框：框 $size，图形 $rect';
            }
            return true;
          }),
      );
      expect(shapes, 3, reason: '品牌标和两张钱包卡片');
    });
  }

  testWidgets('电脑默认窗口里，引导最后一屏的「老用户」链接离窗口底边留出页边距', (tester) async {
    await setWindow(tester, const Size(424, 820));
    await tester.pumpWidget(MaterialApp(
      theme: buildDeepSeaTheme(),
      home: const OnboardingPage(),
    ));
    for (var i = 0; i < 2; i++) {
      await tester.tap(find.text('继续'));
      await tester.pumpAndSettle();
    }
    final restore = tester.getRect(find.ancestor(
      of: find.text('老用户：用账户密钥恢复'),
      matching: find.byType(TextButton),
    ));
    final login = tester.getRect(find.ancestor(
      of: find.text('登录'),
      matching: find.byType(OutlinedButton),
    ));
    expect(820 - restore.bottom, greaterThanOrEqualTo(24));
    expect(restore.top - login.bottom, greaterThanOrEqualTo(4));
  }, variant: TargetPlatformVariant.only(TargetPlatform.windows));
}
