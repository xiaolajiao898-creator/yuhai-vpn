import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/plans/data/plans_api.dart';
import 'package:yuhai_vpn/features/plans/presentation/plans_page.dart';

Widget plansApp(List<Override> overrides) => ProviderScope(
      overrides: overrides,
      child: const MaterialApp(home: PlansPage()),
    );

void main() {
  testWidgets('没有套餐时说明价格尚未上架，并提供重试和客服', (tester) async {
    await tester.pumpWidget(plansApp([
      plansProvider.overrideWith((ref) async => <PlanItem>[]),
    ]));
    await tester.pumpAndSettle();
    expect(find.text('还没有上架套餐'), findsOneWidget);
    expect(find.text('重试'), findsOneWidget);
    expect(find.text('联系客服'), findsOneWidget);
    expect(find.textContaining('USDT'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('套餐加载失败可以重试，不显示编造的价格', (tester) async {
    await tester.pumpWidget(plansApp([
      plansProvider.overrideWith((ref) async => throw Exception('offline')),
    ]));
    await tester.pumpAndSettle();
    expect(find.text('套餐没有加载出来'), findsOneWidget);
    expect(find.text('重试'), findsOneWidget);
    expect(find.textContaining('价格即将'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('时长套餐展示接口返回的 USDT 价格和试用说明', (tester) async {
    await tester.pumpWidget(plansApp([
      plansProvider.overrideWith((ref) async => const [
            PlanItem(
              code: 'M',
              name: '月卡',
              type: 'TIME',
              tier: 1,
              listPrice: '9.9',
              durationDays: 30,
              deviceLimit: 3,
              badge: '推荐',
            ),
          ]),
    ]));
    await tester.pumpAndSettle();
    expect(find.text('新用户免费试用 24 小时'), findsOneWidget);
    expect(find.text('月卡'), findsOneWidget);
    expect(find.textContaining('9.9'), findsOneWidget);
    expect(find.textContaining('不自动续费'), findsWidgets);
    expect(tester.takeException(), isNull);
  });
}
