import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../account/data/account_api.dart';
import '../../settings/presentation/settings_widgets.dart';
import '../data/traffic_api.dart';

const _numberStyle = TextStyle(
  fontFamily: 'Arial Rounded MT Bold',
  fontFamilyFallback: ['SF Pro Rounded', YhFont.numeric, 'sans-serif'],
  fontWeight: FontWeight.w800,
  color: YhColors.moonlight,
);

class TrafficPage extends ConsumerStatefulWidget {
  const TrafficPage({super.key});
  @override
  ConsumerState<TrafficPage> createState() => _TrafficPageState();
}

class _TrafficPageState extends ConsumerState<TrafficPage> {
  int _range = 7, _requestVersion = 0, _selected = 0;
  bool _loading = true, _error = false;
  List<TrafficDay> _days = const [];

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    final version = ++_requestVersion;
    final range = _range;
    setState(() {
      _loading = true;
      _error = false;
      _days = const [];
      _selected = 0;
    });
    try {
      final days = await ref.read(trafficApiProvider).daily(range);
      if (!mounted || version != _requestVersion) return;
      setState(() {
        _days = days;
        _selected = math.max(0, days.length - 1);
        _loading = false;
      });
    } catch (_) {
      if (!mounted || version != _requestVersion) return;
      setState(() {
        _loading = false;
        _error = true;
      });
    }
  }

  void _changeRange(Set<int> value) {
    if (value.single == _range) return;
    setState(() => _range = value.single);
    _load();
  }

  @override
  Widget build(BuildContext context) {
    final devices = ref.watch(devicesProvider);
    final up = _days.fold<int>(0, (sum, day) => sum + day.upBytes);
    final down = _days.fold<int>(0, (sum, day) => sum + day.downBytes);
    final total = up + down;
    final deviceTraffic = aggregateDeviceTraffic(_days);
    final names = {
      for (final device in devices.valueOrNull?.devices ?? <AccountDevice>[])
        device.id: device.displayName
    };

    return Scaffold(
      appBar: AppBar(title: const Text('流量明细'), actions: [
        IconButton(
            tooltip: '刷新流量',
            onPressed: _loading ? null : _load,
            icon: const Icon(Icons.refresh)),
      ]),
      body: RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          physics: const AlwaysScrollableScrollPhysics(),
          padding: const EdgeInsets.fromLTRB(YhSpace.page, 8, YhSpace.page, 32),
          children: [
            Row(children: [
              Expanded(
                  child: Text('用量概览',
                      style: Theme.of(context).textTheme.titleLarge)),
              SegmentedButton<int>(
                segments: const [
                  ButtonSegment(value: 7, label: Text('7 天')),
                  ButtonSegment(value: 30, label: Text('30 天'))
                ],
                selected: {_range},
                showSelectedIcon: false,
                onSelectionChanged: _changeRange,
                style: SegmentedButton.styleFrom(
                    visualDensity: VisualDensity.compact),
              ),
            ]),
            const SizedBox(height: 20),
            if (_loading)
              const Padding(
                  padding: EdgeInsets.symmetric(vertical: 72),
                  child: Center(child: CircularProgressIndicator()))
            else if (_error)
              AccountLoadError(onRetry: _load)
            else ...[
              _SummaryCard(range: _range, total: total, up: up, down: down),
              const SizedBox(height: 20),
              if (total == 0)
                SettingsPanel(
                    child: Padding(
                  padding: const EdgeInsets.all(24),
                  child: Column(children: [
                    const Icon(Icons.data_usage_outlined,
                        size: 36, color: YhColors.haze),
                    const SizedBox(height: 12),
                    const Text('这段时间暂无流量记录'),
                    const SizedBox(height: 8),
                    Text('流量会在节点上报后更新。',
                        style: Theme.of(context).textTheme.labelSmall),
                  ]),
                ))
              else if (_days.isNotEmpty)
                _chartCard(),
              const SizedBox(height: 24),
              Text('设备用量', style: Theme.of(context).textTheme.titleLarge),
              const SizedBox(height: 4),
              Text('最近 $_range 天 · 包含已移除设备的历史用量',
                  style: Theme.of(context).textTheme.labelSmall),
              const SizedBox(height: 12),
              if (deviceTraffic.isEmpty)
                SettingsPanel(
                    child: Padding(
                  padding: const EdgeInsets.all(YhSpace.panel),
                  child: Text(total == 0 ? '暂无设备用量' : '暂未收到设备维度的明细',
                      style: Theme.of(context)
                          .textTheme
                          .bodyMedium
                          ?.copyWith(color: YhColors.haze)),
                ))
              else
                SettingsPanel(
                    child: Column(children: [
                  for (var i = 0; i < deviceTraffic.length; i++) ...[
                    if (i > 0) const Divider(height: 1),
                    _DeviceUsage(
                      usage: deviceTraffic[i],
                      name: names[deviceTraffic[i].deviceId],
                      total: total,
                    ),
                  ],
                ])),
              if (devices.hasError && deviceTraffic.isNotEmpty)
                TextButton(
                  onPressed: () => ref.invalidate(devicesProvider),
                  child: const Text('设备名称未能加载，点击重试'),
                ),
              const SizedBox(height: 24),
              Text('按 UTC 日期统计 · 1 KB = 1024 B\n用量以节点上报为准，可能稍有延迟。',
                  textAlign: TextAlign.center,
                  style: Theme.of(context).textTheme.labelSmall),
            ],
          ],
        ),
      ),
    );
  }

  Widget _chartCard() {
    final day = _days[_selected];
    return SettingsPanel(
        child: Padding(
      padding: const EdgeInsets.all(YhSpace.panel),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Expanded(
              child:
                  Text('每日趋势', style: Theme.of(context).textTheme.titleMedium)),
          Text('点击柱形查看', style: Theme.of(context).textTheme.labelSmall),
        ]),
        const SizedBox(height: 16),
        _TrafficChart(
            days: _days,
            selected: _selected,
            onSelect: (index) => setState(() => _selected = index)),
        const SizedBox(height: 8),
        Row(mainAxisAlignment: MainAxisAlignment.spaceBetween, children: [
          Text(_days.first.shortDate,
              style: Theme.of(context).textTheme.labelSmall),
          Text(_days.last.shortDate,
              style: Theme.of(context).textTheme.labelSmall),
        ]),
        const SizedBox(height: 16),
        const Divider(height: 1),
        Row(children: [
          IconButton(
              tooltip: '前一天',
              onPressed:
                  _selected > 0 ? () => setState(() => _selected--) : null,
              icon: const Icon(Icons.chevron_left)),
          Expanded(
              child: Column(children: [
            Text('${day.date} · UTC',
                key: const ValueKey('traffic-selected-date'),
                style: Theme.of(context).textTheme.labelSmall),
            const SizedBox(height: 4),
            Text(formatTrafficBytes(day.totalBytes),
                key: const ValueKey('traffic-selected-total'),
                style: _numberStyle.copyWith(fontSize: 24)),
          ])),
          IconButton(
              tooltip: '后一天',
              onPressed: _selected < _days.length - 1
                  ? () => setState(() => _selected++)
                  : null,
              icon: const Icon(Icons.chevron_right)),
        ]),
        const SizedBox(height: 8),
        Row(children: [
          Expanded(
              child: _DirectionValue(
                  label: '上传', value: day.upBytes, upload: true)),
          Expanded(
              child: _DirectionValue(
                  label: '下载', value: day.downBytes, upload: false)),
        ]),
      ]),
    ));
  }
}

class _SummaryCard extends StatelessWidget {
  const _SummaryCard(
      {required this.range,
      required this.total,
      required this.up,
      required this.down});
  final int range, total, up, down;

  @override
  Widget build(BuildContext context) => SettingsPanel(
          child: Padding(
        padding: const EdgeInsets.all(YhSpace.panel),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text('最近 $range 天总用量', style: Theme.of(context).textTheme.labelSmall),
          const SizedBox(height: 8),
          Text(formatTrafficBytes(total),
              key: const ValueKey('traffic-total'),
              style: _numberStyle.copyWith(fontSize: 36)),
          const SizedBox(height: 20),
          Row(children: [
            Expanded(
                child: _DirectionValue(label: '上传总计', value: up, upload: true)),
            Expanded(
                child:
                    _DirectionValue(label: '下载总计', value: down, upload: false)),
          ]),
        ]),
      ));
}

class _DirectionValue extends StatelessWidget {
  const _DirectionValue(
      {required this.label, required this.value, required this.upload});
  final String label;
  final int value;
  final bool upload;

  @override
  Widget build(BuildContext context) =>
      Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Icon(upload ? Icons.north_east : Icons.south_west,
              size: 14,
              color: upload ? YhColors.starlight : YhColors.moonlight),
          const SizedBox(width: 4),
          Flexible(
              child:
                  Text(label, style: Theme.of(context).textTheme.labelSmall)),
        ]),
        const SizedBox(height: 4),
        Text(formatTrafficBytes(value),
            style: _numberStyle.copyWith(fontSize: 19)),
      ]);
}

class _DeviceUsage extends StatelessWidget {
  const _DeviceUsage(
      {required this.usage, required this.name, required this.total});
  final DeviceTraffic usage;
  final String? name;
  final int total;

  @override
  Widget build(BuildContext context) => Padding(
        key: ValueKey('traffic-device-${usage.deviceId}'),
        padding: const EdgeInsets.all(YhSpace.panel),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Row(children: [
            const Icon(Icons.devices_outlined, color: YhColors.haze, size: 20),
            const SizedBox(width: 8),
            Expanded(
                child: Text(name ?? '设备 #${usage.deviceId}',
                    maxLines: 2, overflow: TextOverflow.ellipsis)),
            const SizedBox(width: 8),
            Text(formatTrafficBytes(usage.bytes),
                style: _numberStyle.copyWith(fontSize: 16)),
          ]),
          if (name == null)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text('保留历史用量，按设备编号显示',
                  style: Theme.of(context).textTheme.labelSmall),
            ),
          const SizedBox(height: 12),
          ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: LinearProgressIndicator(
                value: total == 0 ? 0 : (usage.bytes / total).clamp(0.0, 1.0),
                minHeight: 4,
                color: YhColors.haze,
                backgroundColor: YhColors.reef,
              )),
        ]),
      );
}

class _TrafficChart extends StatelessWidget {
  const _TrafficChart(
      {required this.days, required this.selected, required this.onSelect});
  final List<TrafficDay> days;
  final int selected;
  final ValueChanged<int> onSelect;

  @override
  Widget build(BuildContext context) => LayoutBuilder(
      builder: (context, constraints) => Semantics(
            label:
                '每日流量柱状图，当前选中 ${days[selected].date}，${formatTrafficBytes(days[selected].totalBytes)}',
            onIncrease: selected < days.length - 1
                ? () => onSelect(selected + 1)
                : null,
            onDecrease: selected > 0 ? () => onSelect(selected - 1) : null,
            child: GestureDetector(
              key: const ValueKey('traffic-chart'),
              behavior: HitTestBehavior.opaque,
              onTapDown: (details) => onSelect((details.localPosition.dx /
                      constraints.maxWidth *
                      days.length)
                  .floor()
                  .clamp(0, days.length - 1)),
              child: CustomPaint(
                  size: Size(constraints.maxWidth, 148),
                  painter: _TrafficPainter(days, selected)),
            ),
          ));
}

class _TrafficPainter extends CustomPainter {
  _TrafficPainter(this.days, this.selected);
  final List<TrafficDay> days;
  final int selected;

  @override
  void paint(Canvas canvas, Size size) {
    final maximum =
        days.fold<int>(0, (max, day) => math.max(max, day.totalBytes));
    final grid = Paint()
      ..color = YhColors.reef
      ..strokeWidth = 1;
    for (var line = 0; line < 4; line++) {
      final y = size.height * line / 3;
      canvas.drawLine(Offset(0, y), Offset(size.width, y), grid);
    }
    final slot = size.width / days.length;
    final width = math.min(24.0, slot * .58);
    for (var index = 0; index < days.length; index++) {
      final day = days[index];
      final x = slot * index + (slot - width) / 2;
      final height =
          maximum == 0 ? 0.0 : day.totalBytes / maximum * (size.height - 8);
      final rect = Rect.fromLTWH(
          x, size.height - math.max(height, 2), width, math.max(height, 2));
      final color = index == selected ? YhColors.starlight : YhColors.haze;
      canvas.drawRRect(RRect.fromRectAndRadius(rect, const Radius.circular(5)),
          Paint()..color = day.totalBytes == 0 ? YhColors.reef : color);
    }
  }

  @override
  bool shouldRepaint(_TrafficPainter oldDelegate) =>
      oldDelegate.days != days || oldDelegate.selected != selected;
}
