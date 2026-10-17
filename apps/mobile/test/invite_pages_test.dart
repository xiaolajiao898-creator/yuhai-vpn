import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/invite/data/invite_api.dart';
import 'package:yuhai_vpn/features/invite/presentation/invite_page.dart';
import 'package:yuhai_vpn/features/invite/presentation/rewards_page.dart';

class FakeInviteApi implements InviteApi {
  final cursors = <String?>[];
  @override
  Ref get ref => throw UnimplementedError();
  @override
  Future<InviteBindResult> bind(
          {required String code,
          required String method,
          required String idempotencyKey}) async =>
      InviteBindResult.fromJson({'bound': true, 'reward_description': '已绑定'});
  @override
  Future<InviteInfo> info() async => InviteInfo.fromJson({
        'invite_code': 'AB1234',
        'invite_url': null,
        'rules': '奖励规则暂未启用',
        'stats': {
          'invited': 2,
          'paid': 1,
          'rewards_pending': 3,
          'rewards_released': 0
        }
      });
  @override
  Future<(List<InviteReward>, String?)> rewards({String? cursor}) async {
    cursors.add(cursor);
    InviteReward reward(String id, String status) => InviteReward.fromJson({
          'id': id,
          'kind': 'BALANCE',
          'status': status,
          'amount': '2.500000',
          'created_at': '2026-09-12T01:00:00Z',
          'release_at': '2026-09-15T01:00:00Z'
        });
    return cursor == null
        ? ([reward('2', 'REVIEW')], 'page2')
        : ([reward('2', 'REVIEW'), reward('1', 'RELEASED')], null);
  }
}

Widget app(FakeInviteApi api, Widget page) => ProviderScope(
    overrides: [inviteApiProvider.overrideWithValue(api)],
    child: MaterialApp(home: page));
void main() {
  testWidgets('未配置官网不展示邀请链接，统计使用笔数', (tester) async {
    await tester.pumpWidget(app(FakeInviteApi(), const InvitePage()));
    await tester.pumpAndSettle();
    expect(find.text('AB1234'), findsOneWidget);
    expect(find.text('复制邀请链接'), findsNothing);
    await tester.scrollUntilVisible(find.text('待处理奖励（笔）'), 200,
        scrollable: find.byType(Scrollable).first);
    await tester.pumpAndSettle();
    expect(find.text('待处理奖励（笔）'), findsOneWidget);
    expect(find.text('已发放奖励（笔）'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
  testWidgets('奖励分页去重且审核中不显示为已发放', (tester) async {
    final api = FakeInviteApi();
    await tester.pumpWidget(app(api, const RewardsPage()));
    await tester.pumpAndSettle();
    expect(find.text('审核中'), findsOneWidget);
    expect(find.text('已发放'), findsNothing);
    await tester.scrollUntilVisible(find.text('加载更多'), 200,
        scrollable: find.byType(Scrollable).first);
    await tester.pumpAndSettle();
    expect(find.text('加载更多').hitTestable(), findsOneWidget);
    await tester.tap(find.text('加载更多'));
    await tester.pumpAndSettle();
    expect(find.text('审核中'), findsOneWidget);
    expect(find.text('已发放'), findsOneWidget);
    expect(api.cursors, [null, 'page2']);
    expect(find.text('已显示全部记录'), findsOneWidget);
  });
}
