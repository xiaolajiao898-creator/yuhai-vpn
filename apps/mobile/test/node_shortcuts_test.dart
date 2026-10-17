import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/countries.dart';
import 'package:yuhai_vpn/features/nodes/data/node_shortcuts.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/presentation/nodes_page.dart';

Map<String, dynamic> line(String id, String cc, String city,
        {bool available = true}) =>
    {
      'id': id,
      'name': '$city · $id',
      'country_code': cc,
      'city': city,
      'region': 'ASIA',
      'available': available,
      'locked_reason': available ? null : 'OFFLINE',
      'load': available ? 20 : null,
      'is_recommended': false,
      'probe': available ? {'host': 'node$id.invalid', 'port': 443} : null,
      'protocols': available ? ['WIREGUARD'] : <String>[],
    };

void main() {
  group('收藏与最近使用', () {
    test('收藏可切换、最近使用去重且最多5条', () {
      var s = const NodeShortcuts();
      s = s.toggleFavorite('1').toggleFavorite('e2');
      expect(s.favorites, ['1', 'e2']);
      s = s.toggleFavorite('1');
      expect(s.favorites, ['e2']);
      for (final id in ['1', '2', '3', '4', '5', '6', '3']) {
        s = s.recordRecent(id);
      }
      expect(s.recents, ['3', '6', '5', '4', '2']);
      expect(s.toggleFavorite('bad id\n'), s);
    });
    test('损坏或越界的记录回到空状态', () {
      final ok = const NodeShortcuts().toggleFavorite('7').recordRecent('8');
      expect(NodeShortcuts.decode(
          '{"version":1,"favorites":["7"],"recents":["8"]}'), ok);
      for (final raw in [
        null,
        'not json',
        '[]',
        '{"version":2,"favorites":[],"recents":[]}',
        '{"version":1,"favorites":["7","7"],"recents":[]}',
        '{"version":1,"favorites":[7],"recents":[]}',
        '{"version":1,"favorites":[],"recents":["1","2","3","4","5","6"]}',
        '{"version":1,"favorites":["a b"],"recents":[]}',
      ]) {
        expect(NodeShortcuts.decode(raw), const NodeShortcuts(), reason: raw);
      }
    });
  });

  group('国家与搜索', () {
    test('中文、英文、代码、城市均可搜', () {
      bool m(String q) =>
          matchesQuery(query: q, countryCode: 'JP', city: '大阪', name: '大阪 · 01');
      expect(m('日本'), isTrue);
      expect(m('japan'), isTrue);
      expect(m('JP'), isTrue);
      expect(m('大阪'), isTrue);
      expect(m('日本 大阪'), isTrue);
      expect(m('美国'), isFalse);
      expect(m('  '), isTrue);
      expect(countryFlag('SG'), '🇸🇬');
      expect(countryFlag('sg'), '');
      expect(countryName(''), '未标注地区');
    });
  });

  testWidgets('节点页：新加坡/美国/日本置顶，搜索无结果有说明，可收藏', (tester) async {
    tester.view.physicalSize = const Size(900, 3200);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final list = parseNodes({
      'nodes': [
        line('1', 'HK', '香港'),
        line('2', 'JP', '东京'),
        line('3', 'US', '洛杉矶'),
        line('4', 'SG', '新加坡'),
        line('5', 'US', '纽约', available: false),
      ]
    });
    final storage = MemoryNodeShortcutsStorage();
    final container = ProviderContainer(overrides: [
      nodesProvider.overrideWith((ref) async => list),
      nodeShortcutsStoreProvider.overrideWithValue(storage),
    ]);
    addTearDown(container.dispose);
    await tester.pumpWidget(UncontrolledProviderScope(
        container: container, child: const MaterialApp(home: NodesPage())));
    await tester.pumpAndSettle();

    // 置顶地区的顺序：新加坡、美国、日本，其余在后
    final sg = tester.getTopLeft(find.textContaining('🇸🇬  新加坡')).dy;
    final us = tester.getTopLeft(find.textContaining('🇺🇸  美国')).dy;
    final jp = tester.getTopLeft(find.textContaining('🇯🇵  日本')).dy;
    final hk = tester.getTopLeft(find.textContaining('🇭🇰  中国香港')).dy;
    expect(sg < us && us < jp && jp < hk, isTrue);
    expect(find.text('1/2 可用'), findsOneWidget); // 美国：1 条可用 1 条离线
    expect(find.text('节点暂时离线'), findsOneWidget);

    await tester.enterText(find.byType(TextField), '火星');
    await tester.pumpAndSettle();
    expect(find.text('没有找到「火星」相关线路'), findsOneWidget);

    await tester.enterText(find.byType(TextField), 'tokyo 东京');
    await tester.pumpAndSettle();
    expect(find.text('没有找到「tokyo 东京」相关线路'), findsOneWidget);

    await tester.enterText(find.byType(TextField), '东京');
    await tester.pumpAndSettle();
    expect(find.text('东京 · 2'), findsOneWidget);
    expect(find.text('香港 · 1'), findsNothing);
    await tester.tap(find.byTooltip('收藏'));
    await tester.pumpAndSettle();
    expect((await storage.load()).favorites, ['2']);
    expect(find.byTooltip('取消收藏'), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
  });
}
