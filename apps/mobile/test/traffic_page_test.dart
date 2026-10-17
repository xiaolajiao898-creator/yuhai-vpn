import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/features/account/data/account_api.dart';
import 'package:yuhai_vpn/features/traffic/data/traffic_api.dart';
import 'package:yuhai_vpn/features/traffic/presentation/traffic_page.dart';

class FakeTrafficApi implements TrafficApi {
  FakeTrafficApi(this.handle);
  final Future<List<TrafficDay>> Function(int) handle;
  final requests = <int>[];
  @override
  Ref get ref => throw UnimplementedError();
  @override
  Future<List<TrafficDay>> daily(int days) {
    requests.add(days);
    return handle(days);
  }
}

TrafficDay day(String date, int up, int down,
        {List<DeviceTraffic> devices = const []}) =>
    TrafficDay(date: date, upBytes: up, downBytes: down, devices: devices);

Widget app(FakeTrafficApi api) => ProviderScope(
        overrides: [
          trafficApiProvider.overrideWithValue(api),
          devicesProvider.overrideWith((_) async => DeviceList([
                AccountDevice.fromJson({
                  'id': '1',
                  'display_name': '我的手机',
                  'platform': 'IOS',
                  'is_current': true
                }),
              ], 3)),
        ],
        child:
            MaterialApp(theme: buildDeepSeaTheme(), home: const TrafficPage()));

void main() {
  testWidgets('7/30天切换，旧请求迟到不能覆盖新数据', (tester) async {
    final first = Completer<List<TrafficDay>>();
    final api = FakeTrafficApi((days) => days == 7
        ? first.future
        : Future.value([day('2026-09-12', 1024, 3072)]));
    await tester.pumpWidget(app(api));
    await tester.pump();
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    await tester.tap(find.text('30 天'));
    await tester.pumpAndSettle();
    expect(find.text('最近 30 天总用量'), findsOneWidget);
    expect(
        tester.widget<Text>(find.byKey(const ValueKey('traffic-total'))).data,
        '4 KB');
    first.complete([day('2026-09-10', 0, 1024 * 1024)]);
    await tester.pumpAndSettle();
    expect(
        tester.widget<Text>(find.byKey(const ValueKey('traffic-total'))).data,
        '4 KB');
    expect(api.requests, [7, 30]);
    expect(tester.takeException(), isNull);
  });

  testWidgets('旧请求迟到失败不影响已经加载的30天数据', (tester) async {
    final first = Completer<List<TrafficDay>>();
    final api = FakeTrafficApi((days) =>
        days == 7 ? first.future : Future.value([day('2026-09-12', 12, 34)]));
    await tester.pumpWidget(app(api));
    await tester.pump();
    await tester.tap(find.text('30 天'));
    await tester.pumpAndSettle();
    first.completeError(Exception('old offline request'));
    await tester.pumpAndSettle();
    expect(find.text('重新加载'), findsNothing);
    expect(
        tester.widget<Text>(find.byKey(const ValueKey('traffic-total'))).data,
        '46 B');
    expect(tester.takeException(), isNull);
  });

  testWidgets('每日柱状图可选择日期，360px显示设备历史且无横向溢出', (tester) async {
    tester.view.physicalSize = const Size(360, 800);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    final api = FakeTrafficApi((_) async => [
          day('2026-09-11', 1024, 2048, devices: [
            const DeviceTraffic(deviceId: '1', bytes: 1024),
            const DeviceTraffic(deviceId: '99', bytes: 2048)
          ]),
          day('2026-09-12', 2048, 4096,
              devices: [const DeviceTraffic(deviceId: '1', bytes: 6144)]),
        ]);
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    expect(
        tester
            .widget<Text>(find.byKey(const ValueKey('traffic-selected-date')))
            .data,
        '2026-09-12 · UTC');
    final chart = find.byKey(const ValueKey('traffic-chart'));
    await tester.ensureVisible(chart);
    final rect = tester.getRect(chart);
    await tester.tapAt(Offset(rect.left + rect.width * .25, rect.center.dy));
    await tester.pumpAndSettle();
    expect(
        tester
            .widget<Text>(find.byKey(const ValueKey('traffic-selected-date')))
            .data,
        '2026-09-11 · UTC');
    expect(
        tester
            .widget<Text>(find.byKey(const ValueKey('traffic-selected-total')))
            .data,
        '3 KB');
    await tester.ensureVisible(find.text('设备 #99'));
    expect(find.text('我的手机'), findsOneWidget);
    expect(find.text('设备 #99'), findsOneWidget);
    expect(find.text('保留历史用量，按设备编号显示'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  for (final zeroFilled in [false, true]) {
    testWidgets(zeroFilled ? '服务端全零记录显示真实空态' : '空记录不伪造流量', (tester) async {
      final api = FakeTrafficApi(
          (_) async => zeroFilled ? [day('2026-09-12', 0, 0)] : []);
      await tester.pumpWidget(app(api));
      await tester.pumpAndSettle();
      expect(
          tester.widget<Text>(find.byKey(const ValueKey('traffic-total'))).data,
          '0 B');
      expect(find.text('这段时间暂无流量记录'), findsOneWidget);
      expect(find.text('暂无设备用量'), findsOneWidget);
      expect(find.byKey(const ValueKey('traffic-chart')), findsNothing);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('流量请求失败显示重试，重试使用当前周期', (tester) async {
    var failed = true;
    final api = FakeTrafficApi((_) async {
      if (failed) throw Exception('network offline');
      return [day('2026-09-12', 100, 200)];
    });
    await tester.pumpWidget(app(api));
    await tester.pumpAndSettle();
    expect(find.text('重新加载'), findsOneWidget);
    failed = false;
    await tester.tap(find.text('重新加载'));
    await tester.pumpAndSettle();
    expect(
        tester.widget<Text>(find.byKey(const ValueKey('traffic-total'))).data,
        '300 B');
    expect(api.requests, [7, 7]);
  });

  test('流量单位按1024换算，保留两位小数且不加逗号', () {
    expect(formatTrafficBytes(0), '0 B');
    expect(formatTrafficBytes(1023), '1023 B');
    expect(formatTrafficBytes(1024), '1 KB');
    expect(formatTrafficBytes(1280), '1.25 KB');
    expect(formatTrafficBytes(1024 * 1024), '1 MB');
    expect(formatTrafficBytes(1024 * 1024 * 1024), '1 GB');
    expect(formatTrafficBytes(1024 * 1024 * 1024 * 1024), '1 TB');
    expect(() => formatTrafficBytes(-1), throwsArgumentError);
  });

  test('设备用量跨日期聚合保留已移除设备，日期和字节数格式必须有效', () {
    final usage = aggregateDeviceTraffic([
      day('2026-09-11', 100, 200,
          devices: [const DeviceTraffic(deviceId: '9', bytes: 100)]),
      day('2026-09-12', 100, 200,
          devices: [const DeviceTraffic(deviceId: '9', bytes: 200)]),
    ]);
    expect(usage.single.deviceId, '9');
    expect(usage.single.bytes, 300);
    expect(
        () => TrafficDay.fromJson(
            {'date': '2026-02-31', 'up_bytes': 0, 'down_bytes': 0}),
        throwsFormatException);
    expect(
        () => TrafficDay.fromJson(
            {'date': '2026-09-12', 'up_bytes': -1, 'down_bytes': 0}),
        throwsFormatException);
  });
}
