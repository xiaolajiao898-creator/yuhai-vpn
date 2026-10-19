import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:yuhai_vpn/app/router.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/features/invite/data/invite_api.dart';
import 'package:yuhai_vpn/features/invite/presentation/invite_clipboard.dart';
import 'package:yuhai_vpn/features/invite/presentation/invite_page.dart';
import 'package:yuhai_vpn/features/invite/presentation/reward_format.dart';
import 'package:yuhai_vpn/features/invite/presentation/rewards_page.dart';
import 'package:yuhai_vpn/preview/preview_api.dart';

class BindCall {
  const BindCall(this.code, this.method, this.key);
  final String code, method, key;
}

class BindingApi implements InviteApi {
  String status = 'AVAILABLE';
  bool bindingEnabled = true, rewardsEnabled = false, failSummary = false;
  int summaries = 0;
  final calls = <BindCall>[];
  Future<InviteBindResult> Function(BindCall)? respond;
  Future<(List<InviteReward>, String?)> Function(String?)? rewardResponse;
  @override
  Ref get ref => throw UnimplementedError();
  @override
  Future<InviteInfo> info() async {
    summaries++;
    if (failSummary) throw Exception('summary offline');
    return InviteInfo.fromJson({
      'invite_code': 'ABCD23',
      'invite_url': null,
      'rules': '以实际发放记录为准',
      'binding_enabled': bindingEnabled,
      'rewards_enabled': rewardsEnabled,
      'can_bind': status == 'AVAILABLE',
      'bind_status': status,
      'bind_deadline': '2028-09-12T00:00:00Z',
      'stats': {
        'invited': 0,
        'paid': 0,
        'rewards_pending': 0,
        'rewards_released': 0
      },
    });
  }

  @override
  Future<InviteBindResult> bind(
      {required String code,
      required String method,
      required String idempotencyKey}) async {
    final call = BindCall(code, method, idempotencyKey);
    calls.add(call);
    if (respond != null) return respond!(call);
    status = 'BOUND';
    return InviteBindResult.fromJson(
        {'bound': true, 'reward_description': '已绑定，等待满足活动规则。'});
  }

  @override
  Future<(List<InviteReward>, String?)> rewards({String? cursor}) async =>
      rewardResponse == null
          ? (<InviteReward>[], null)
          : rewardResponse!(cursor);
}

class FakeClipboard extends InviteClipboard {
  int reads = 0;
  final copied = <String>[];
  Future<String?> Function()? read;
  @override
  Future<String?> readText() async {
    reads++;
    return read == null ? '宇海邀请码 BCD234' : read!();
  }

  @override
  Future<void> writeText(String value) async {
    copied.add(value);
  }
}

Widget app(BindingApi api,
        {Widget page = const InvitePage(), FakeClipboard? clipboard}) =>
    ProviderScope(overrides: [
      inviteApiProvider.overrideWithValue(api),
      inviteClipboardProvider.overrideWithValue(clipboard ?? FakeClipboard()),
      inviteNowProvider.overrideWithValue(() => DateTime.utc(2026, 9, 12)),
    ], child: MaterialApp(theme: buildDeepSeaTheme(), home: page));

Future<void> reveal(WidgetTester tester, Finder target) async {
  final scrollable = find.byType(Scrollable).first;
  if (target.evaluate().isEmpty) {
    // ListView 尚未构建远处子项时先回到列表起点，再真实滚动查找。
    // 不用拉到越界的手势，避免误触发 RefreshIndicator 改变测试状态。
    final position = tester.state<ScrollableState>(scrollable).position;
    position.jumpTo(position.minScrollExtent);
    await tester.pumpAndSettle();
  }
  await tester.scrollUntilVisible(target, 200,
      scrollable: scrollable, maxScrolls: 30);
  await tester.pumpAndSettle();
  await Scrollable.ensureVisible(tester.element(target), alignment: .5);
  await tester.pumpAndSettle();
  expect(target.hitTestable(), findsOneWidget);
}

Future<void> enterCode(WidgetTester tester, String code) async {
  final input = find.byKey(const ValueKey('invite-bind-code'));
  await reveal(tester, input);
  await tester.enterText(input, code);
  await tester.pumpAndSettle();
}

Future<void> confirmBind(WidgetTester tester) async {
  await reveal(tester, find.text('绑定邀请码'));
  await tester.tap(find.text('绑定邀请码'));
  await tester.pumpAndSettle();
  expect(find.textContaining('绑定后不能修改'), findsWidgets);
  await tester.tap(find.text('确认绑定'));
  await tester.pumpAndSettle();
}

InviteReward reward(String id, String kind, String status,
        {int? days, int? bytes, String? amount}) =>
    InviteReward.fromJson({
      'id': id,
      'kind': kind,
      'status': status,
      'days': days,
      'bytes': bytes,
      'amount': amount,
      'created_at': '2026-09-12T00:00:00Z',
      'release_at': '2026-09-15T00:00:00Z',
    });

void main() {
  test('演示可绑定ABC234，奖励保持关闭且奖励数据标记为示例', () async {
    final api = PreviewApi(PreviewSessionStore());
    addTearDown(() => api.dio.close(force: true));
    final summary = await api.call('GET', '/v1/invite');
    expect(summary['binding_enabled'], isTrue);
    expect(summary['rewards_enabled'], isFalse);
    final bound = await api.call('POST', '/v1/invite/bind',
        json: {'code': 'ABC234', 'method': 'MANUAL'},
        sign: true,
        idempotencyKey: 'c65dd0c4-bd53-4011-8b27-9d4a9f6a3597');
    expect(bound['bound'], isTrue);
    final after = await api.call('GET', '/v1/invite');
    expect(after['bind_status'], 'BOUND');
    expect(after['rewards_enabled'], isFalse);
    final history = await api.call('GET', '/v1/invite/rewards');
    expect(
        (history['items'] as List).every((item) => item['is_example'] == true),
        isTrue);
    expect((history['items'] as List).isNotEmpty, isTrue);
  });

  test('奖励开关默认关闭且不改变邀请码绑定准入', () {
    final data = {
      'invite_code': 'ABCD23',
      'invite_url': null,
      'rules': '',
      'stats': <String, int>{},
      'binding_enabled': true,
      'can_bind': true,
      'bind_status': 'AVAILABLE',
    };
    final info = InviteInfo.fromJson(data);
    expect(info.rewardsEnabled, isFalse);
    expect(info.acceptsBinding, isTrue);
    expect(
        InviteInfo.fromJson({...data, 'rewards_enabled': true}).rewardsEnabled,
        isTrue);
  });

  testWidgets('奖励关闭仍能绑定，确认时说明当前不发放奖励', (tester) async {
    final api = BindingApi()..rewardsEnabled = false;
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    expect(find.text('邀请绑定已开放'), findsOneWidget);
    expect(find.text('奖励活动暂未开放'), findsOneWidget);
    await enterCode(tester, 'ABC234');
    await reveal(tester, find.text('绑定邀请码'));
    await tester.tap(find.text('绑定邀请码'));
    await tester.pumpAndSettle();
    expect(find.textContaining('当前仅绑定邀请关系，不发放奖励。'), findsWidgets);
    expect(api.calls, isEmpty);
    await tester.tap(find.text('确认绑定'));
    await tester.pumpAndSettle();
    expect(api.calls.single.code, 'ABC234');
    await reveal(tester, find.text('邀请码绑定成功'));
    expect(find.text('邀请码绑定成功'), findsOneWidget);
    await reveal(tester, find.text('奖励活动暂未开放'));
    expect(find.text('奖励活动暂未开放'), findsOneWidget);
  });

  testWidgets('奖励关闭不阻止进入历史记录，也不隐藏已发放奖励', (tester) async {
    final api = BindingApi()..rewardsEnabled = false;
    api.rewardResponse =
        (_) async => ([reward('history', 'DAYS', 'RELEASED', days: 7)], null);
    final router = GoRouter(initialLocation: Routes.invite, routes: [
      GoRoute(path: Routes.invite, builder: (_, __) => const InvitePage()),
      GoRoute(path: Routes.rewards, builder: (_, __) => const RewardsPage()),
    ]);
    addTearDown(router.dispose);
    await tester.pumpWidget(ProviderScope(
        overrides: [
          inviteApiProvider.overrideWithValue(api),
        ],
        child: MaterialApp.router(
            theme: buildDeepSeaTheme(), routerConfig: router)));
    await tester.pumpAndSettle();
    await reveal(tester, find.text('查看奖励记录'));
    await tester.tap(find.text('查看奖励记录'));
    await tester.pumpAndSettle();
    expect(find.text('奖励活动暂未开放，历史记录仍可查看。'), findsOneWidget);
    expect(find.text('7 天'), findsOneWidget);
    expect(find.text('已发放'), findsOneWidget);
  });

  testWidgets('活动状态读取失败也可查看历史奖励', (tester) async {
    final api = BindingApi()..failSummary = true;
    api.rewardResponse =
        (_) async => ([reward('history', 'DAYS', 'RELEASED', days: 7)], null);
    await tester.pumpWidget(app(api, page: const RewardsPage()));
    await tester.pumpAndSettle();
    expect(find.text('7 天'), findsOneWidget);
    expect(find.text('已发放'), findsOneWidget);
    expect(find.text('加载失败，点击重试'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  test('邀请码只接受指定六位字符及两种分享文本', () {
    expect(parseInviteCode(' abcd23 '), 'ABCD23');
    expect(parseInviteCode('宇海邀请码 BCD234'), 'BCD234');
    expect(parseInviteCode('宇海VPN邀请码 BCD234'), 'BCD234');
    expect(parseInviteCode('YHINVITE:BCD234'), 'BCD234');
    for (final invalid in [
      'A0CD23',
      'A1CD23',
      'AICD23',
      'ALCD23',
      'AOCD23',
      'hello BCD234',
      'BCD234abc',
      '123456'
    ]) {
      expect(parseInviteCode(invalid), isNull, reason: invalid);
    }
  });

  for (final entry in {
    'DISABLED': '邀请码绑定暂未开放',
    'EXPIRED': '绑定期限已结束',
    'BOUND': '已绑定邀请码',
    'PAID': '已完成付费，无法补绑',
    'BLOCKED': '暂时无法绑定'
  }.entries) {
    testWidgets('${entry.key}不可继续提交绑定', (tester) async {
      final api = BindingApi()..status = entry.key;
      await tester.pumpWidget(app(api));
      await tester.pumpAndSettle();
      await reveal(tester, find.text(entry.value));
      expect(find.text(entry.value), findsOneWidget);
      expect(find.byKey(const ValueKey('invite-bind-code')), findsNothing);
      expect(api.calls, isEmpty);
    });
  }

  testWidgets('只在明确点按时读取剪贴板，绑定前必须二次确认', (tester) async {
    final api = BindingApi();
    final clipboard = FakeClipboard();
    await tester.pumpWidget(app(api, clipboard: clipboard));
    await tester.pumpAndSettle();
    expect(clipboard.reads, 0);
    await reveal(tester, find.text('粘贴邀请码'));
    await tester.tap(find.text('粘贴邀请码'));
    await tester.pumpAndSettle();
    expect(clipboard.reads, 1);
    expect(
        tester
            .widget<TextField>(find.byKey(const ValueKey('invite-bind-code')))
            .controller!
            .text,
        'BCD234');
    await reveal(tester, find.text('绑定邀请码'));
    await tester.tap(find.text('绑定邀请码'));
    await tester.pumpAndSettle();
    expect(api.calls, isEmpty);
    await tester.tap(find.text('再检查一下'));
    await tester.pumpAndSettle();
    expect(api.calls, isEmpty);
    await confirmBind(tester);
    expect(api.calls.single.method, 'CLIPBOARD');
    expect(api.calls.single.code, 'BCD234');
    expect(find.text('邀请码绑定成功'), findsOneWidget);
    expect(api.summaries, 2);
  });

  testWidgets('失败相同payload复用UUID，改邀请码生成新UUID', (tester) async {
    final api = BindingApi();
    api.respond = (_) async {
      if (api.calls.length <= 2) throw Exception('network uncertain');
      api.status = 'BOUND';
      return InviteBindResult.fromJson(
          {'bound': true, 'reward_description': '已绑定'});
    };
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    await enterCode(tester, 'BCD234');
    await confirmBind(tester);
    expect(find.text('绑定结果暂未确认，请检查网络后重试。'), findsOneWidget);
    await confirmBind(tester);
    await enterCode(tester, 'CDE234');
    await confirmBind(tester);
    expect(api.calls.map((call) => call.code), ['BCD234', 'BCD234', 'CDE234']);
    expect(api.calls[0].key, api.calls[1].key);
    expect(api.calls[0].key, isNot(api.calls[2].key));
    expect(
        RegExp(r'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$')
            .hasMatch(api.calls[0].key),
        isTrue);
  });

  testWidgets('提交中禁止连点且刷新失败仍保留已绑定确认', (tester) async {
    final api = BindingApi();
    final pending = Completer<InviteBindResult>();
    api.respond = (_) => pending.future;
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    await enterCode(tester, 'BCD234');
    await confirmBind(tester);
    expect(api.calls, hasLength(1));
    expect(
        tester
            .widget<FilledButton>(find.widgetWithText(FilledButton, '正在绑定…'))
            .onPressed,
        isNull);
    api.failSummary = true;
    pending.complete(InviteBindResult.fromJson(
        {'bound': true, 'reward_description': '已绑定'}));
    await tester.pumpAndSettle();
    expect(find.text('邀请码绑定成功'), findsOneWidget);
    expect(find.text('重新加载'), findsOneWidget);
    expect(find.byKey(const ValueKey('invite-bind-code')), findsNothing);
  });

  testWidgets('剪贴板迟到不能覆盖用户手动输入', (tester) async {
    final api = BindingApi();
    final clipboard = FakeClipboard();
    final pending = Completer<String?>();
    clipboard.read = () => pending.future;
    await tester.pumpWidget(app(api, clipboard: clipboard));
    await tester.pumpAndSettle();
    await reveal(tester, find.text('粘贴邀请码'));
    await tester.tap(find.text('粘贴邀请码'));
    await tester.pump();
    expect(clipboard.reads, 1);
    expect(find.text('正在读取…'), findsOneWidget);
    await enterCode(tester, 'CDE234');
    pending.complete('YHINVITE:BCD234');
    await tester.pumpAndSettle();
    expect(
        tester
            .widget<TextField>(find.byKey(const ValueKey('invite-bind-code')))
            .controller!
            .text,
        'CDE234');
    await confirmBind(tester);
    expect(api.calls.single.code, 'CDE234');
    expect(api.calls.single.method, 'MANUAL');
  });

  testWidgets('无效字符及自己的邀请码不能提交', (tester) async {
    final api = BindingApi();
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    for (final code in ['A0CD23', 'ABCD23']) {
      await enterCode(tester, code);
      await reveal(tester, find.text('绑定邀请码'));
      await tester.tap(find.text('绑定邀请码'));
      await tester.pumpAndSettle();
      expect(find.text('确认绑定'), findsNothing);
      expect(api.calls, isEmpty);
    }
  });

  testWidgets('奖励为空时不伪造收益', (tester) async {
    await tester.pumpWidget(app(BindingApi(), page: const RewardsPage()));
    await tester.pumpAndSettle();
    expect(find.text('暂无奖励记录'), findsOneWidget);
    expect(find.text('0 USDT'), findsNothing);
  });

  testWidgets('奖励刷新后旧分页迟到不得混入新列表', (tester) async {
    final api = BindingApi();
    final pending = Completer<(List<InviteReward>, String?)>();
    var initial = 0;
    api.rewardResponse = (cursor) async {
      if (cursor != null) return pending.future;
      initial++;
      return initial == 1
          ? ([reward('1', 'DAYS', 'PENDING', days: 7)], 'next')
          : ([reward('2', 'DAYS', 'RELEASED', days: 3)], null);
    };
    await tester.pumpWidget(app(api, page: const RewardsPage()));
    await tester.pumpAndSettle();
    await reveal(tester, find.text('加载更多'));
    await tester.tap(find.text('加载更多'));
    await tester.pump();
    await tester.tap(find.byTooltip('刷新奖励'));
    await tester.pumpAndSettle();
    pending.complete(([reward('3', 'DAYS', 'REVIEW', days: 99)], null));
    await tester.pumpAndSettle();
    expect(find.text('3 天'), findsOneWidget);
    expect(find.text('99 天'), findsNothing);
    expect(find.text('7 天'), findsNothing);
  });

  test('奖励状态和天数GBUSDT格式不将冻结奖励冒充到账', () {
    final pending = reward('1', 'DAYS', 'PENDING', days: 7);
    expect(inviteRewardStatus(pending, DateTime.utc(2026, 9, 12)), '冻结中');
    expect(inviteRewardStatus(pending, DateTime.utc(2026, 9, 16)), '待发放');
    expect(inviteRewardValue(pending), '7 天');
    expect(
        inviteRewardValue(
            reward('2', 'TRAFFIC', 'RELEASED', bytes: 5368709120)),
        '5 GB');
    expect(
        inviteRewardValue(
            reward('3', 'BALANCE', 'RELEASED', amount: '1200.500000')),
        '1200.5 USDT');
    expect(
        inviteRewardValue(reward('4', 'BALANCE', 'REJECTED', amount: '1200')),
        '1200 USDT');
    expect(
        inviteRewardStatus(
            reward('5', 'DAYS', 'REVIEW', days: 1), DateTime.utc(2026)),
        '审核中');
    expect(
        inviteRewardStatus(
            reward('6', 'DAYS', 'REJECTED', days: 1), DateTime.utc(2026)),
        '已取消');
  });
}
