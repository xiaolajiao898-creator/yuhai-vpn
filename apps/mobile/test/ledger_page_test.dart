import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/account/data/ledger_api.dart';
import 'package:yuhai_vpn/features/account/presentation/ledger_page.dart';

class FakeLedgerApi implements LedgerApi {
  FakeLedgerApi(this.fetch);
  final Future<LedgerBatch> Function(bool, String?) fetch;
  @override
  Ref get ref => throw UnimplementedError();
  @override
  Future<LedgerBatch> list(bool entitlements, {String? cursor}) => fetch(entitlements, cursor);
}
Widget app(FakeLedgerApi api) => ProviderScope(overrides: [ledgerApiProvider.overrideWithValue(api)], child: const MaterialApp(home: LedgerPage()));
LedgerEntry entry(String id, String amount) => LedgerEntry(id: id, kind: 'DEPOSIT', createdAt: DateTime.utc(2026,9,12), amount: amount);

void main() {
  testWidgets('分页不丢旧账单，金额不添加千分位', (tester) async {
    final calls = <String?>[];
    await tester.pumpWidget(app(FakeLedgerApi((_, cursor) async {
      calls.add(cursor);
      return cursor == null ? LedgerBatch([entry('2','1200.000000')], 'next') : LedgerBatch([entry('1','1.000001')], null);
    })));
    await tester.pumpAndSettle();
    expect(find.text('+1200 USDT'), findsOneWidget);
    await tester.tap(find.text('加载更多')); await tester.pumpAndSettle();
    expect(find.text('+1200 USDT'), findsOneWidget);
    expect(find.text('+1.000001 USDT'), findsOneWidget);
    expect(calls, [null, 'next']);
    expect(find.text('已显示全部记录'), findsOneWidget);
  });
  testWidgets('切换账单类型后忽略旧请求返回', (tester) async {
    final old = Completer<LedgerBatch>();
    await tester.pumpWidget(app(FakeLedgerApi((entitlements, _) => entitlements ? Future.value(const LedgerBatch([], null)) : old.future)));
    await tester.pump();
    await tester.tap(find.text('权益记录')); await tester.pumpAndSettle();
    old.complete(LedgerBatch([entry('1','999.000000')], null)); await tester.pumpAndSettle();
    expect(find.text('暂无记录'), findsOneWidget);
    expect(find.text('+999 USDT'), findsNothing);
    expect(tester.takeException(), isNull);
  });
  testWidgets('失败后可以重试', (tester) async {
    var count = 0;
    await tester.pumpWidget(app(FakeLedgerApi((_, __) async {
      if (++count == 1) throw StateError('offline');
      return const LedgerBatch([], null);
    })));
    await tester.pumpAndSettle();
    await tester.tap(find.text('暂时无法加载，请重试')); await tester.pumpAndSettle();
    expect(find.text('暂无记录'), findsOneWidget);
    expect(count, 2);
  });
}
