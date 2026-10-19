import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/account/data/trial_api.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
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
    expect(find.text('还没有可购买的套餐'), findsOneWidget);
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
      ..._newUserTrial(),
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
    expect(find.text('30 天 · 不限流量 · 3 台设备'), findsOneWidget);
    await tester.pumpWidget(plansApp([
      ..._newUserTrial(),
      plansProvider.overrideWith((ref) async => const [
            PlanItem(code: 'Q', name: 'L1 九十天', type: 'TIME', tier: 1, listPrice: '16', durationDays: 90, deviceLimit: 3),
          ]),
    ]));
    await tester.pumpAndSettle();
    expect(find.text('季卡 · 90 天 · 不限流量 · 3 台设备'), findsOneWidget);
    expect(find.textContaining('9.9'), findsOneWidget);
    expect(find.textContaining('不自动续费'), findsOneWidget);
    expect(find.text('回首页领取'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('套餐页可以直接领取试用，并写上到期时间', (tester) async {
    final expiry = DateTime.utc(2026, 10, 20, 2, 30);
    await tester.pumpWidget(plansApp([
      ..._newUserTrial(),
      trialApiProvider.overrideWith((ref) => _FixedTrial(ref, expiry)),
      plansProvider.overrideWith((ref) async => const [
            PlanItem(code: 'W', name: '周卡', type: 'TIME', tier: 1, listPrice: '4.9', durationDays: 7, deviceLimit: 3),
          ]),
    ]));
    await tester.pumpAndSettle();
    await tester.tap(find.text('免费试用 24 小时'));
    await tester.pumpAndSettle();
    expect(find.text('试用已开通'), findsOneWidget);
    expect(find.textContaining('结束'), findsOneWidget);
    expect(find.text('免费试用 24 小时'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('已有套餐或试用已用过时不显示试用卡', (tester) async {
    await tester.pumpWidget(plansApp([
      meProvider.overrideWith((ref) async => const {
            'entitlement': {'plan_name': '月卡', 'tier': 1},
          }),
      trialStatusProvider.overrideWith((ref) async => const TrialStatus(
            enabled: true,
            eligible: false,
            status: 'ineligible',
            reason: 'NOT_NEW',
            tier: 1,
            deviceLimit: 3,
            unlimited: true,
            durationHours: 24,
          )),
      plansProvider.overrideWith((ref) async => const [
            PlanItem(code: 'M', name: '月卡', type: 'TIME', tier: 1, listPrice: '12', durationDays: 30, deviceLimit: 3),
          ]),
    ]));
    await tester.pumpAndSettle();
    expect(find.text('新用户免费试用 24 小时'), findsNothing);
    expect(find.text('月卡'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}

List<Override> _newUserTrial() => [
      meProvider.overrideWith((ref) async => const {
            'entitlement': {'blocked_reason': 'NO_PLAN'},
          }),
      trialStatusProvider.overrideWith((ref) async => const TrialStatus(
            enabled: true,
            eligible: true,
            status: 'available',
            tier: 1,
            deviceLimit: 3,
            unlimited: true,
            durationHours: 24,
          )),
    ];

class _FixedTrial extends TrialApi {
  _FixedTrial(super.ref, this.expiry);
  final DateTime expiry;

  @override
  Future<TrialGrant> claim() async => TrialGrant(
        status: 'granted',
        expireAt: expiry,
        tier: 1,
        deviceLimit: 3,
        unlimited: true,
        durationHours: 24,
        active: true,
      );
}
