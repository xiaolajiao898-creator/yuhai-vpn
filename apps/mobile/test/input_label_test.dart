import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/app/theme/tokens.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/auth_layout.dart';

/// 浮动标签必须整段落在填充框里面，不能骑在顶边上。
void expectLabelInsideField(WidgetTester tester, String label) {
  final labelRect = tester.getRect(find.text(label));
  final field = find.ancestor(
    of: find.text(label),
    matching: find.byType(InputDecorator),
  );
  final fieldRect = tester.getRect(field.first);
  expect(labelRect.top, greaterThanOrEqualTo(fieldRect.top + 4),
      reason: '$label 压到了输入框顶边');
  expect(labelRect.bottom, lessThanOrEqualTo(fieldRect.bottom - 4),
      reason: '$label 超出了输入框');
  final editable =
      find.descendant(of: field.first, matching: find.byType(EditableText));
  expect(labelRect.bottom, lessThanOrEqualTo(tester.getRect(editable).top + 1),
      reason: '$label 和输入文字叠在一起');
}

Future<void> pumpRegisterForm(
  WidgetTester tester, {
  required TargetPlatform platform,
  required Size size,
}) async {
  debugDefaultTargetPlatformOverride = platform;
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(() {
    tester.view.resetPhysicalSize();
    tester.view.resetDevicePixelRatio();
  });
  final address =
      TextEditingController(text: 'TGw3itAxfGjYzc4jk4XB34BNoZcxpq4byt');
  final again = TextEditingController(text: 'secret12');
  addTearDown(() {
    address.dispose();
    again.dispose();
  });
  try {
    await tester.pumpWidget(MaterialApp(
      theme: buildDeepSeaTheme(),
      home: Scaffold(
        body: AuthLayout(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Text('创建账户', style: ThemeData.dark().textTheme.headlineMedium),
              const SizedBox(height: YhSpace.page),
              TextField(
                controller: address,
                style: const TextStyle(
                    fontFamily: YhFont.numeric, letterSpacing: 0.4),
                decoration: const InputDecoration(
                  labelText: 'USDT 地址（TRC20）',
                  hintText: 'T 开头，共 34 位',
                ),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              const TextField(
                obscureText: true,
                decoration: InputDecoration(
                  labelText: '设置密码',
                  helperText: '至少 8 位，包含字母和数字',
                ),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              TextField(
                controller: again,
                obscureText: true,
                decoration: const InputDecoration(labelText: '再输一次密码'),
              ),
            ],
          ),
        ),
      ),
    ));
    // 有内容的框标签保持浮动；空的密码框点一下，复现截图里的浮动状态。
    await tester.tap(find.widgetWithText(TextField, '设置密码'));
    await tester.pumpAndSettle();
  } finally {
    // 测试框架在 tearDown 之前就检查这个变量，必须在测试体里清掉。
    debugDefaultTargetPlatformOverride = null;
  }
}

void main() {
  testWidgets('安卓手机上浮动标签落在填充框内', (tester) async {
    await pumpRegisterForm(
      tester,
      platform: TargetPlatform.android,
      size: const Size(390, 844),
    );
    expectLabelInsideField(tester, 'USDT 地址（TRC20）');
    expectLabelInsideField(tester, '设置密码');
    expectLabelInsideField(tester, '再输一次密码');
    expect(find.text('至少 8 位，包含字母和数字'), findsOneWidget);
  });

  testWidgets('Windows 宽窗口上浮动标签落在填充框内', (tester) async {
    await pumpRegisterForm(
      tester,
      platform: TargetPlatform.windows,
      size: const Size(1280, 800),
    );
    expectLabelInsideField(tester, 'USDT 地址（TRC20）');
    expectLabelInsideField(tester, '设置密码');
    expectLabelInsideField(tester, '再输一次密码');
  });
}
