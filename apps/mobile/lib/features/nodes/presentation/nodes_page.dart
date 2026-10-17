import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/theme/tokens.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../connection/presentation/connection_controller.dart';
import '../data/countries.dart';
import '../data/node_shortcuts.dart';
import '../data/nodes_api.dart';
import '../data/node_probe.dart';
import '../data/node_selection.dart';
export '../data/node_selection.dart';

/// 线路选择：搜索、自动选择、收藏、最近使用，按国家 → 城市 → 线路展示。
/// 新加坡、美国、日本置顶（只在确有线路时出现）。延迟是「本机到线路」的 TCP 往返，
/// 不是下载速度；没有检测过就不显示数字。
class NodesPage extends ConsumerStatefulWidget {
  const NodesPage({super.key});
  @override
  ConsumerState<NodesPage> createState() => _NodesPageState();
}

class _NodesPageState extends ConsumerState<NodesPage>
    with WidgetsBindingObserver {
  Timer? _refreshTimer;
  var _generation = 0;
  var _probing = false, _refreshing = true;
  var _foreground = true;
  String? _probeMessage;
  final _search = TextEditingController();
  String _query = '';

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (mounted) _refresh();
    });
    _refreshTimer = Timer.periodic(const Duration(seconds: 30), (_) {
      if (mounted &&
          _foreground &&
          ModalRoute.of(context)?.isCurrent == true &&
          !_refreshing) {
        _refresh();
      }
    });
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    _foreground = state == AppLifecycleState.resumed;
    if (_foreground && mounted && ModalRoute.of(context)?.isCurrent == true) {
      _refresh();
    }
    if (!_foreground) {
      _generation++;
      if (mounted) setState(() => _probing = false);
    }
  }

  @override
  void dispose() {
    _generation++;
    _refreshTimer?.cancel();
    _search.dispose();
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  Future<void> _refresh() async {
    _generation++;
    if (!mounted) return;
    setState(() {
      _refreshing = true;
      _probing = false;
      _probeMessage = null;
    });
    ref.read(latencyProvider.notifier).state = {};
    final generation = _generation;
    try {
      final pending = ref.refresh(nodesProvider.future);
      await pending;
    } catch (_) {/* rendered by AsyncValue */}
    if (mounted && generation == _generation) {
      setState(() => _refreshing = false);
    }
  }

  Future<void> _probeAll(List<NodeItem> nodes) async {
    if (_probing || _refreshing) return;
    final generation = ++_generation;
    setState(() {
      _probing = true;
      _probeMessage = null;
    });
    final engine = ref.read(vpnEngineProvider);
    bool current() =>
        mounted &&
        _foreground &&
        generation == _generation &&
        !ref.read(nodesProvider).isLoading &&
        identical(ref.read(nodesProvider).asData?.value, nodes);
    final results = await probeNodes(
        nodes, (host, port) => engine.probe(host, port),
        isCurrent: current);
    if (current()) {
      ref.read(latencyProvider.notifier).state = results;
      final t = TimeOfDay.now();
      final hh = t.hour.toString().padLeft(2, '0');
      final mm = t.minute.toString().padLeft(2, '0');
      _probeMessage =
          '$hh:$mm 从本机检测了 ${results.length} 条线路的延迟 · 每轮最多20条 · 延迟不代表下载速度';
    }
    if (mounted && generation == _generation) setState(() => _probing = false);
  }

  void _choose(String? id) {
    ref.read(selectedNodeProvider.notifier).state = id;
    Navigator.pop(context);
  }

  @override
  Widget build(BuildContext context) {
    final nodes = ref.watch(nodesProvider);
    final selected = ref.watch(selectedNodeProvider);
    final latency = ref.watch(latencyProvider);
    final shortcuts =
        ref.watch(nodeShortcutsProvider).valueOrNull ?? const NodeShortcuts();
    final text = Theme.of(context).textTheme;
    final canProbe = ref.watch(vpnEngineProvider).canProbe;
    final listReady = !_refreshing && !nodes.isLoading && nodes.asData != null;
    return Scaffold(
      appBar: AppBar(
        title: const Text('选择线路'),
        actions: [
          IconButton(
              icon: const Icon(Icons.refresh),
              tooltip: '刷新线路',
              onPressed: _refreshing ? null : _refresh),
          IconButton(
              icon: const Icon(Icons.network_ping),
              tooltip: !canProbe
                  ? '延迟检测暂不可用'
                  : (_probing ? '正在检测延迟' : '延迟检测（最多20条）'),
              onPressed: listReady && canProbe && !_probing
                  ? () => _probeAll(nodes.asData!.value)
                  : null),
        ],
      ),
      body: _refreshing
          ? const Center(child: CircularProgressIndicator())
          : nodes.when(
              skipLoadingOnRefresh: false,
              skipLoadingOnReload: false,
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (_, __) => Center(
                  child: Column(mainAxisSize: MainAxisSize.min, children: [
                Text('无法更新线路，请刷新后再选择',
                    style: text.bodyMedium?.copyWith(color: YhColors.danger)),
                const SizedBox(height: 12),
                FilledButton.tonal(
                    onPressed: _refresh, child: const Text('重新加载')),
              ])),
              data: (list) => _body(context, list, selected, latency, shortcuts),
            ),
    );
  }

  Widget _body(BuildContext context, List<NodeItem> list, String? selected,
      Map<String, int?> latency, NodeShortcuts shortcuts) {
    final text = Theme.of(context).textTheme;
    final byId = {for (final n in list) n.id: n};
    final chosen = byId[selected];
    final autoAvailable =
        list.any((n) => n.available && !n.isEgress && n.probeHost != null);
    final matches = list
        .where((n) => matchesQuery(
            query: _query,
            countryCode: n.countryCode,
            city: n.city,
            name: n.name))
        .toList();
    final searching = _query.trim().isNotEmpty;

    Widget tile(NodeItem n) => _LineTile(
          node: n,
          latency: latency,
          selected: selected == n.id,
          favorite: shortcuts.favorites.contains(n.id),
          onTap: () => _choose(n.id),
          onFavorite: () {
            // 先取好提示入口，异步失败回来时不再使用可能已失效的 context。
            final messenger = ScaffoldMessenger.of(context);
            unawaited(ref
                .read(nodeShortcutsProvider.notifier)
                .toggleFavorite(n.id)
                .catchError((Object _) {
              if (mounted) {
                messenger.showSnackBar(
                    const SnackBar(content: Text('收藏没有保存成功，请重试')));
              }
            }));
          },
        );

    final featured = [
      for (final c in featuredCountries)
        if (list.any((n) => n.countryCode == c)) c
    ];
    final children = <Widget>[
      if (selected != null && (chosen == null || !chosen.available))
        Padding(
            padding: const EdgeInsets.only(bottom: 12),
            child: Text('已选线路暂不可用，请重新选择',
                style: text.bodyMedium?.copyWith(color: YhColors.warn))),
      TextField(
        controller: _search,
        onChanged: (v) => setState(() => _query = v),
        textInputAction: TextInputAction.search,
        decoration: InputDecoration(
          hintText: '搜索国家、城市或线路',
          prefixIcon: const Icon(Icons.search, color: YhColors.sub),
          suffixIcon: searching
              ? IconButton(
                  tooltip: '清空搜索',
                  icon: const Icon(Icons.close, color: YhColors.sub),
                  onPressed: () => setState(() {
                        _search.clear();
                        _query = '';
                      }))
              : null,
        ),
      ),
      const SizedBox(height: 12),
      if (!searching) ...[
        if (featured.isNotEmpty)
          Wrap(spacing: 8, runSpacing: 8, children: [
            for (final c in featured)
              ActionChip(
                avatar: Text(countryFlag(c)),
                label: Text(countryName(c)),
                backgroundColor: YhColors.soft,
                side: const BorderSide(color: YhColors.line),
                onPressed: () => setState(() {
                  _search.text = countryName(c);
                  _query = _search.text;
                }),
              ),
          ]),
        const SizedBox(height: 12),
        _AutoTile(
            available: autoAvailable,
            selected: selected == null,
            onTap: autoAvailable ? () => _choose(null) : null),
        if (_probing) const LinearProgressIndicator(),
        if (_probeMessage != null)
          Padding(
              padding: const EdgeInsets.only(top: 8),
              child: Text(_probeMessage!, style: text.labelSmall)),
        _Section(
            title: '收藏',
            empty: shortcuts.favorites.isEmpty
                ? null
                : '收藏的线路已下架，可在列表中重新收藏',
            children: [
              for (final id in shortcuts.favorites)
                if (byId[id] != null) tile(byId[id]!)
            ]),
        _Section(title: '最近使用', children: [
          for (final id in shortcuts.recents)
            if (byId[id] != null) tile(byId[id]!)
        ]),
      ],
      if (!searching && list.isEmpty)
        Padding(
            padding: const EdgeInsets.symmetric(vertical: 32),
            child: Center(
                child: Text('目前还没有可用线路\n后台添加后，这里会自动出现',
                    textAlign: TextAlign.center,
                    style: text.bodyMedium?.copyWith(color: YhColors.sub)))),
      if (searching && matches.isEmpty)
        Padding(
            padding: const EdgeInsets.symmetric(vertical: 32),
            child: Center(
                child: Text('没有找到「${_query.trim()}」相关线路',
                    style: text.bodyMedium?.copyWith(color: YhColors.sub)))),
      for (final group in _byCountry(matches))
        _Section(
            title: group.$1.isEmpty
                ? countryName('')
                : '${countryFlag(group.$1)}  ${countryName(group.$1)}',
            trailing: '${group.$2.where((n) => n.available).length}/${group.$2.length} 可用',
            children: [for (final n in group.$2) tile(n)]),
      const SizedBox(height: 8),
      Text('每30秒自动核实 · 下拉也可刷新', style: text.labelSmall),
      const SizedBox(height: 24),
    ];
    return RefreshIndicator(
        onRefresh: _refresh,
        child: ListView(
          physics: const AlwaysScrollableScrollPhysics(),
          padding: const EdgeInsets.symmetric(
              horizontal: YhSpace.page, vertical: YhSpace.unit),
          children: children,
        ));
  }

  /// 置顶地区在前，其余按中文名排序；同一国家内按城市、再按名称排序。
  List<(String, List<NodeItem>)> _byCountry(List<NodeItem> list) {
    final groups = <String, List<NodeItem>>{};
    for (final n in list) {
      groups.putIfAbsent(n.countryCode, () => []).add(n);
    }
    int rank(String c) {
      final i = featuredCountries.indexOf(c);
      return i < 0 ? featuredCountries.length : i;
    }

    final keys = groups.keys.toList()
      ..sort((a, b) {
        final r = rank(a).compareTo(rank(b));
        return r != 0 ? r : countryName(a).compareTo(countryName(b));
      });
    return [
      for (final k in keys)
        (
          k,
          groups[k]!
            ..sort((a, b) {
              final c = a.city.compareTo(b.city);
              return c != 0 ? c : a.name.compareTo(b.name);
            })
        )
    ];
  }
}

class _Section extends StatelessWidget {
  const _Section(
      {required this.title, required this.children, this.trailing, this.empty});
  final String title;
  final String? trailing, empty;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    if (children.isEmpty && empty == null) return const SizedBox.shrink();
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.only(top: YhSpace.page),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Row(children: [
          Expanded(
              child: Text(title,
                  style: text.bodyMedium?.copyWith(
                      color: YhColors.ink, fontWeight: FontWeight.w600))),
          if (trailing != null) Text(trailing!, style: text.labelSmall),
        ]),
        const SizedBox(height: YhSpace.unit),
        if (children.isEmpty && empty != null)
          Text(empty!, style: text.labelSmall)
        else
          ...children,
      ]),
    );
  }
}

class _AutoTile extends StatelessWidget {
  const _AutoTile(
      {required this.available, required this.selected, required this.onTap});
  final bool available, selected;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => _Frame(
        selected: selected,
        enabled: available,
        onTap: onTap,
        child: ListTile(
          leading: const Icon(Icons.bolt, color: YhColors.accent),
          title: const Text('自动选择'),
          subtitle: Text(
              available ? '连接时从本机检测延迟，选最低的可用 WireGuard 线路' : '暂时没有可自动选择的线路',
              style: Theme.of(context).textTheme.labelSmall),
        ),
      );
}

class _LineTile extends StatelessWidget {
  const _LineTile(
      {required this.node,
      required this.latency,
      required this.selected,
      required this.favorite,
      required this.onTap,
      required this.onFavorite});
  final NodeItem node;
  final Map<String, int?> latency;
  final bool selected, favorite;
  final VoidCallback onTap, onFavorite;

  @override
  Widget build(BuildContext context) {
    final n = node;
    final text = Theme.of(context).textTheme;
    final ms = latency[n.id];
    final details = n.available
        ? [
            if (n.city.isNotEmpty) n.city,
            if (n.isEgress) egressProtocolLabel(n.protocols.first),
            if (n.isEgress && n.supportsUdp == false) '不支持 UDP',
            if (ms != null) '延迟 $ms ms',
            if (latency.containsKey(n.id) && ms == null) '本次检测未响应',
            if (n.load != null) '负载 ${n.load}%',
          ].join(' · ')
        : n.unavailableLabel;
    return _Frame(
      selected: selected,
      enabled: n.available,
      onTap: n.available ? onTap : null,
      child: ListTile(
        leading: Text(countryFlag(n.countryCode),
            style: const TextStyle(fontSize: 22)),
        title: Row(children: [
          Flexible(
              child: Text(n.name,
                  style: text.bodyLarge, overflow: TextOverflow.ellipsis)),
          if (n.isRecommended && n.available) ...[
            const SizedBox(width: 6),
            const _Badge('推荐'),
          ],
        ]),
        subtitle: Text(details,
            style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric)),
        trailing: n.available
            ? IconButton(
                tooltip: favorite ? '取消收藏' : '收藏',
                onPressed: onFavorite,
                icon: Icon(favorite ? Icons.star : Icons.star_border,
                    color: favorite ? YhColors.accent : YhColors.sub))
            : const Icon(Icons.lock_outline, color: YhColors.sub, size: 18),
      ),
    );
  }
}

class _Badge extends StatelessWidget {
  const _Badge(this.label);
  final String label;
  @override
  Widget build(BuildContext context) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
        decoration: BoxDecoration(
            color: YhColors.soft,
            borderRadius: BorderRadius.circular(YhRadius.tag)),
        child: Text(label,
            style: const TextStyle(fontSize: 11, color: YhColors.accent)),
      );
}

class _Frame extends StatelessWidget {
  const _Frame(
      {required this.child,
      required this.selected,
      required this.enabled,
      required this.onTap});
  final Widget child;
  final bool selected, enabled;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) => Opacity(
        opacity: enabled ? 1 : 0.55,
        child: Padding(
          padding: const EdgeInsets.only(bottom: YhSpace.unit),
          child: Material(
            color: selected ? YhColors.soft : YhColors.paper,
            shape: RoundedRectangleBorder(
                side: BorderSide(
                    color: selected ? YhColors.accent : YhColors.line),
                borderRadius: BorderRadius.circular(YhRadius.listSelected)),
            clipBehavior: Clip.antiAlias,
            child: InkWell(onTap: onTap, child: child),
          ),
        ),
      );
}
