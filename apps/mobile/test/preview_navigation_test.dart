import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/preview_main.dart';

void main() {
  testWidgets('预览在手机宽度可查看设备，模拟移除后能重置示例账户', (tester) async {
    tester.view.physicalSize = const Size(390, 844);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(const PreviewHost());
    await tester.pumpAndSettle();
    expect(find.text('轻触连接'), findsOneWidget);
    await tester.tap(find.text('我的'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('设备管理'));
    await tester.pumpAndSettle();
    expect(find.text('2 / 3'), findsOneWidget);
    await tester.tap(find.text('移除设备'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('确认移除'));
    await tester.pumpAndSettle();
    expect(find.text('1 / 3'), findsOneWidget);
    await tester.tap(find.text('重置演示'));
    await tester.pumpAndSettle();
    expect(find.text('轻触连接'), findsOneWidget);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
    await tester.pumpAndSettle();
  });
}
