import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_probe.dart';
import 'package:yuhai_vpn/features/nodes/presentation/nodes_page.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';

Map<String, dynamic> row(
        {String id = '9007199254740993', bool available = true}) =>
    {
      'id': id,
      'name': '香港 · 测试',
      'country_code': 'HK',
      'city': '香港',
      'region': 'ASIA',
      'available': available,
      'locked_reason': available ? null : 'OFFLINE',
      'load': available ? 20 : null,
      'is_recommended': false,
      'probe': available ? {'host': 'node.invalid', 'port': 443} : null,
      'protocols': available ? ['WIREGUARD'] : <String>[],
    };

class PendingProbeEngine extends MockVpnEngine {
  final pending = <Completer<int?>>[];
  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) {
    final c = Completer<int?>();
    pending.add(c);
    return c.future;
  }
}

void main() {
  test('信号按探测耗时分档，不表示带宽', () {
    expect(signalLabel(80), '信号好');
    expect(signalLabel(150), '信号中');
    expect(signalLabel(300), '信号弱');
    expect(signalLabel(900), '信号差');
    expect(signalLabel(null), isEmpty);
  });

  test('节点不完整、重复、假协议和混合可用状态拒绝；空列表保留', () {
    expect(parseNodes({'nodes': <dynamic>[]}), isEmpty);
    expect(() => parseNodes({}), throwsFormatException);
    final n = parseNodes({
      'nodes': [row()]
    }).single;
    expect(n.id, '9007199254740993');
    expect(() => n.protocols.add('MOCK'), throwsUnsupportedError);
    expect(
        () => parseNodes({
              'nodes': [row(), row()]
            }),
        throwsFormatException);
    expect(
        () => parseNodes(
            {'nodes': List.generate(1001, (i) => row(id: '${i + 1}'))}),
        throwsFormatException);
    for (final change in <Map<String, dynamic>>[
      {'id': 9007199254740993},
      {'id': '01'},
      {'id': '9223372036854775808'},
      {'load': 101},
      {'available': 'true'},
      {'probe': null},
      {
        'protocols': ['MOCK']
      },
      {'protocols': []},
      {
        'probe': {'host': 'node.invalid/path', 'port': 443}
      },
      {
        'probe': {'host': 'node.invalid', 'port': 0}
      },
      {'locked_reason': 'OFFLINE'},
      {'is_recommended': null},
    ]) {
      expect(
          () => parseNodes({
                'nodes': [
                  {...row(), ...change}
                ]
              }),
          throwsFormatException);
    }
    expect(
        () => parseNodes({
              'nodes': [
                {...row(available: false), 'load': 0}
              ]
            }),
        throwsFormatException);
    expect(
        parseNodes({
          'nodes': [
            {
              ...row(id: 'demo'),
              'protocols': ['MOCK']
            }
          ]
        }, allowMock: true)
            .single
            .id,
        'demo');
  });
  test('测速最多20条、并发4条，只测可用线路', () async {
    final nodes = [
      NodeItem.fromJson(row(available: false)),
      ...List.generate(25, (i) => NodeItem.fromJson(row(id: '${i + 1}')))
    ];
    var active = 0, peak = 0, calls = 0;
    final results = await probeNodes(nodes, (host, port) async {
      expect(host, 'node.invalid');
      expect(port, 443);
      calls++;
      active++;
      if (active > peak) peak = active;
      await Future<void>.delayed(Duration.zero);
      active--;
      return 25;
    }, isCurrent: () => true);
    expect(calls, 20);
    expect(results.length, 20);
    expect(peak, 4);
    expect(active, 0);
  });
  test('取消不再调度剩余线路，超时和负延迟不能显示成功', () async {
    final nodes =
        List.generate(10, (i) => NodeItem.fromJson(row(id: '${i + 1}')));
    var current = true;
    var calls = 0;
    final pending = <Completer<int?>>[];
    final operation = probeNodes(nodes, (_, __) {
      calls++;
      final c = Completer<int?>();
      pending.add(c);
      return c.future;
    }, isCurrent: () => current);
    await Future<void>.delayed(Duration.zero);
    current = false;
    for (final c in pending) {
      c.complete(12);
    }
    expect(await operation, isEmpty);
    expect(calls, 4);
    final never = Completer<int?>();
    final timeout = await probeNodes([nodes.first], (_, __) => never.future,
        isCurrent: () => true, timeout: const Duration(milliseconds: 10));
    expect(timeout, {'1': null});
    never.complete(1);
    expect(
        await probeNodes([nodes.first], (_, __) async => -1,
            isCurrent: () => true),
        {'1': null});
  });
  testWidgets('刷新撤下旧线路并忽略迟到测速；失败可以重载', (tester) async {
    var list = [NodeItem.fromJson(row())];
    var fail = false;
    final engine = PendingProbeEngine();
    final container = ProviderContainer(overrides: [
      nodesProvider.overrideWith((ref) async {
        if (fail) throw StateError('offline');
        return list;
      }),
      vpnEngineProvider.overrideWithValue(engine),
    ]);
    addTearDown(container.dispose);
    addTearDown(engine.dispose);
    container.read(selectedNodeProvider.notifier).state = list.first.id;
    await tester.pumpWidget(UncontrolledProviderScope(
        container: container, child: const MaterialApp(home: NodesPage())));
    await tester.pumpAndSettle();
    await tester.tap(find.byTooltip('延迟检测（最多20条）'));
    await tester.pump();
    expect(engine.pending.length, 1);
    list = [NodeItem.fromJson(row(available: false))];
    await tester.tap(find.byTooltip('刷新线路'));
    await tester.pumpAndSettle();
    expect(find.text('节点暂时离线'), findsOneWidget);
    expect(find.text('已选线路暂不可用，请重新选择'), findsOneWidget);
    engine.pending.single.complete(88);
    await tester.pump();
    expect(container.read(latencyProvider), isEmpty);
    expect(find.textContaining('88 ms'), findsNothing);
    fail = true;
    await tester.tap(find.byTooltip('刷新线路'));
    await tester.pumpAndSettle();
    expect(find.text('无法更新线路，请刷新后再选择'), findsOneWidget);
    expect(find.text('香港 · 测试'), findsNothing);
    fail = false;
    await tester.tap(find.text('重新加载'));
    await tester.pumpAndSettle();
    expect(find.text('节点暂时离线'), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
  });
  testWidgets('打开的节点页会定时重新核实，关闭后不继续测速', (tester) async {
    var list = [NodeItem.fromJson(row())];
    var reads = 0;
    final container = ProviderContainer(overrides: [
      nodesProvider.overrideWith((ref) async {
        reads++;
        return list;
      })
    ]);
    addTearDown(container.dispose);
    await tester.pumpWidget(UncontrolledProviderScope(
        container: container, child: const MaterialApp(home: NodesPage())));
    await tester.pumpAndSettle();
    final initial = reads;
    list = [NodeItem.fromJson(row(available: false))];
    await tester.pump(const Duration(seconds: 31));
    await tester.pumpAndSettle();
    expect(reads, greaterThan(initial));
    expect(find.text('节点暂时离线'), findsOneWidget);
    await tester.pumpWidget(const SizedBox.shrink());
    final closed = reads;
    await tester.pump(const Duration(seconds: 31));
    expect(reads, closed);
  });
}
