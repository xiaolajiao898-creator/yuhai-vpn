import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/auth_layout.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/register_page.dart';

/// 登录注册页的底部操作区固定不滚动：内容再长，主按钮也在窗口里。
void main() {
  Future<void> setWindow(WidgetTester tester, Size size) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
  }

  Widget tallContent() => Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          for (var i = 0; i < 30; i++)
            SizedBox(height: 60, child: Text('第 $i 行')),
        ],
      );

  testWidgets('内容比窗口高时，底部按钮仍在窗口里，内容可以滚动', (tester) async {
    await setWindow(tester, const Size(424, 600));
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: AuthLayout(
          footer: FilledButton(onPressed: () {}, child: const Text('提交')),
          child: tallContent(),
        ),
      ),
    ));
    final button = tester.getRect(find.text('提交'));
    expect(button.top, greaterThanOrEqualTo(0));
    expect(button.bottom, lessThanOrEqualTo(600));
    // 最后一行一开始在按钮下面（看不见），滚到底以后出现在按钮上方，按钮位置不变。
    expect(tester.getRect(find.text('第 29 行')).top, greaterThan(button.top));

    await tester.drag(
        find.byType(SingleChildScrollView), const Offset(0, -3000));
    await tester.pump();
    expect(tester.getRect(find.text('第 29 行')).bottom,
        lessThanOrEqualTo(button.top));
    expect(tester.getRect(find.text('提交')), button);
  });

  testWidgets('内容不高时，内容和底部按钮一起居中', (tester) async {
    await setWindow(tester, const Size(424, 800));
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(
        body: AuthLayout(
          footer: FilledButton(onPressed: () {}, child: const Text('提交')),
          child: const SizedBox(height: 100, child: Text('标题')),
        ),
      ),
    ));
    final title = tester.getRect(find.text('标题'));
    final button = tester.getRect(find.text('提交'));
    expect(title.top, greaterThan(100));
    expect(button.top, greaterThan(title.bottom));
    expect(800 - button.bottom, greaterThan(100));
  });

  testWidgets('不给底部操作区时，排版和原来一样', (tester) async {
    await setWindow(tester, const Size(424, 600));
    await tester.pumpWidget(MaterialApp(
      home: Scaffold(body: AuthLayout(child: tallContent())),
    ));
    final inLayout = find.descendant(
        of: find.byType(AuthLayout), matching: find.byType(Flexible));
    expect(inLayout, findsNothing);
    expect(find.byType(SingleChildScrollView), findsOneWidget);
  });

  testWidgets('注册页在电脑默认窗口里，「注册并登录」和「去登录」都看得见', (tester) async {
    // 默认窗口 440×860，减去标题栏和边框后，页面能用的大约是 424×820。
    await setWindow(tester, const Size(424, 820));
    await tester.pumpWidget(const ProviderScope(
      child: MaterialApp(home: RegisterPage()),
    ));
    await tester.pump();
    for (final label in ['注册并登录', '已有账户？去登录']) {
      final rect = tester.getRect(find.text(label));
      expect(rect.top, greaterThanOrEqualTo(0), reason: label);
      expect(rect.bottom, lessThanOrEqualTo(820), reason: label);
    }
  });
}
