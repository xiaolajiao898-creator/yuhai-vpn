import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/widgets/tide_ring_button.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../../platform/vpn/vpn_engine.dart';
import '../../connection/presentation/connection_controller.dart';
import '../data/me_api.dart';
import '../../nodes/data/countries.dart';
import '../../nodes/presentation/nodes_page.dart';
import '../../nodes/data/nodes_api.dart';
import '../../settings/data/connection_preferences.dart';

/// 引擎上报的本次连接统计。只有引擎确认已连接后才会有数据，界面不自行编造。
final vpnStatsProvider = StreamProvider.autoDispose<VpnStats>(
    (ref) => ref.watch(vpnEngineProvider).stats);

/// 首页：状态 → 连接按钮 → 当前线路卡 → 账户提示。
/// 「已连接」、计时与速率都只来自原生引擎的确认事件；失败给出原因和可执行的下一步。
class HomePage extends ConsumerStatefulWidget {
  const HomePage({super.key});
  @override
  ConsumerState<HomePage> createState() => _HomePageState();
}

class _HomePageState extends ConsumerState<HomePage> {
  Timer? _tick;

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  void _syncTicker(bool connected) {
    if (connected && _tick == null) {
      _tick = Timer.periodic(const Duration(seconds: 1), (_) {
        if (mounted) setState(() {});
      });
    } else if (!connected && _tick != null) {
      _tick!.cancel();
      _tick = null;
    }
  }

  @override
  Widget build(BuildContext context) {
    final status = ref.watch(connectionControllerProvider);
    final demo = ref.watch(vpnDemoModeProvider);
    final failure = ref.watch(connectionFailureProvider);
    final notice = ref.watch(connectionNoticeProvider);
    final me = ref.watch(meProvider);
    final selectedNode = ref.watch(selectedNodeProvider);
    final nodeState = ref.watch(nodesProvider);
    final stats = status == VpnStatus.connected
        ? ref.watch(vpnStatsProvider).valueOrNull
        : null;
    _syncTicker(status == VpnStatus.connected && stats != null);
    final chosen =
        nodeState.asData?.value.where((n) => n.id == selectedNode).firstOrNull;
    final text = Theme.of(context).textTheme;
    final reduceMotion = ref.watch(reduceConnectionMotionProvider);
    final (label, color) = switch (status) {
      VpnStatus.connected => (demo ? '模拟已连接' : '已连接', YhColors.good),
      VpnStatus.connecting => ('正在连接', YhColors.accent),
      VpnStatus.reconnecting => ('正在重连', YhColors.warn),
      VpnStatus.disconnecting => ('正在断开', YhColors.sub),
      VpnStatus.error => ('连接失败', YhColors.danger),
      VpnStatus.disconnected => ('未连接', YhColors.sub),
    };
    return Scaffold(
      appBar: AppBar(title: const Text('宇海'), actions: [
        TextButton(
            onPressed: () => context.push(Routes.balance),
            child: const Text('账单')),
        IconButton(
            tooltip: '设置',
            onPressed: () => context.push(Routes.settings),
            icon: const Icon(Icons.settings_outlined))
      ]),
      bottomNavigationBar: NavigationBar(
        selectedIndex: 0,
        onDestinationSelected: (index) {
          if (index == 1) context.push(Routes.plans);
          if (index == 2) context.push(Routes.invite);
          if (index == 3) context.push(Routes.settings);
        },
        destinations: const [
          NavigationDestination(
              icon: Icon(Icons.shield_outlined),
              selectedIcon: Icon(Icons.shield),
              label: '连接'),
          NavigationDestination(
              icon: Icon(Icons.workspace_premium_outlined), label: '套餐'),
          NavigationDestination(
              icon: Icon(Icons.card_giftcard_outlined), label: '邀请'),
          NavigationDestination(icon: Icon(Icons.person_outline), label: '我的'),
        ],
      ),
      body: SafeArea(
        child: ListView(
          padding: const EdgeInsets.symmetric(horizontal: YhSpace.page),
          children: [
            if (demo)
              Padding(
                  padding: const EdgeInsets.only(top: 8),
                  child: Text('演示模式 · 示例数据，不会建立真实 VPN 连接',
                      textAlign: TextAlign.center,
                      style: text.labelSmall?.copyWith(color: YhColors.warn))),
            const SizedBox(height: 16),
            Row(mainAxisAlignment: MainAxisAlignment.center, children: [
              Container(
                  width: 8,
                  height: 8,
                  decoration:
                      BoxDecoration(color: color, shape: BoxShape.circle)),
              const SizedBox(width: 8),
              Text(label, style: text.titleLarge?.copyWith(color: color)),
            ]),
            if (stats != null) ...[
              const SizedBox(height: 4),
              Text(_duration(DateTime.now().toUtc().difference(stats.since)),
                  textAlign: TextAlign.center,
                  style: text.bodyLarge?.copyWith(
                      fontFamily: YhFont.numeric,
                      fontFeatures: const [FontFeature.tabularFigures()])),
            ],
            const SizedBox(height: 24),
            MediaQuery(
              data: MediaQuery.of(context).copyWith(
                  disableAnimations:
                      MediaQuery.of(context).disableAnimations || reduceMotion),
              child: Center(
                child: TideRingButton(
                  status: status,
                  onTap: () =>
                      ref.read(connectionControllerProvider.notifier).toggle(),
                ),
              ),
            ),
            const SizedBox(height: 16),
            if (stats != null)
              Row(mainAxisAlignment: MainAxisAlignment.center, children: [
                _Rate(icon: Icons.arrow_downward, value: stats.downSpeed),
                const SizedBox(width: 24),
                _Rate(icon: Icons.arrow_upward, value: stats.upSpeed),
              ]),
            if (failure != null && status == VpnStatus.error)
              _FailureCard(
                message: failure.message,
                onRetry: () =>
                    ref.read(connectionControllerProvider.notifier).toggle(),
                onSwitch: () => context.push(Routes.nodes),
              ),
            if (notice != null &&
                (status == VpnStatus.connected ||
                    status == VpnStatus.connecting))
              Padding(
                padding: const EdgeInsets.only(top: 12),
                child: Text(notice,
                    textAlign: TextAlign.center,
                    style: text.labelSmall?.copyWith(color: YhColors.warn)),
              ),
            const SizedBox(height: 24),
            _LineCard(
                selectedId: selectedNode,
                chosen: chosen,
                loading: nodeState.isLoading,
                onTap: () => context.push(Routes.nodes)),
            const SizedBox(height: 12),
            _AccountCard(me: me),
            const SizedBox(height: 8),
            Align(
                alignment: Alignment.centerLeft,
                child: TextButton.icon(
                  onPressed: () => context.push(Routes.traffic),
                  icon: const Icon(Icons.bar_chart_rounded, size: 18),
                  label: const Text('查看流量明细'),
                )),
            const SizedBox(height: 16),
          ],
        ),
      ),
    );
  }

  static String _duration(Duration d) {
    if (d.isNegative) d = Duration.zero;
    String two(int v) => v.toString().padLeft(2, '0');
    return '${two(d.inHours)}:${two(d.inMinutes % 60)}:${two(d.inSeconds % 60)}';
  }
}

class _Rate extends StatelessWidget {
  const _Rate({required this.icon, required this.value});
  final IconData icon;
  final int value;

  static String format(int bytesPerSecond) {
    const units = ['B/s', 'KB/s', 'MB/s', 'GB/s'];
    var v = bytesPerSecond.toDouble();
    var i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return '${v.toStringAsFixed(i == 0 ? 0 : 1)} ${units[i]}';
  }

  @override
  Widget build(BuildContext context) => Row(children: [
        Icon(icon, size: 14, color: YhColors.sub),
        const SizedBox(width: 4),
        Text(format(value),
            style: Theme.of(context)
                .textTheme
                .labelSmall
                ?.copyWith(fontFamily: YhFont.numeric)),
      ]);
}

class _FailureCard extends StatelessWidget {
  const _FailureCard(
      {required this.message, required this.onRetry, required this.onSwitch});
  final String message;
  final VoidCallback onRetry, onSwitch;

  @override
  Widget build(BuildContext context) => Container(
        margin: const EdgeInsets.only(top: 12),
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: YhColors.paper,
          borderRadius: BorderRadius.circular(YhRadius.surface),
          border: Border.all(color: YhColors.danger.withValues(alpha: .4)),
        ),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Text(message,
              style: Theme.of(context)
                  .textTheme
                  .bodyMedium
                  ?.copyWith(color: YhColors.danger)),
          const SizedBox(height: 12),
          Row(children: [
            Expanded(
                child: OutlinedButton(
                    onPressed: onRetry, child: const Text('重试'))),
            const SizedBox(width: 12),
            Expanded(
                child: OutlinedButton(
                    onPressed: onSwitch, child: const Text('换一条线路'))),
          ]),
        ]),
      );
}

class _LineCard extends StatelessWidget {
  const _LineCard(
      {required this.selectedId,
      required this.chosen,
      required this.loading,
      required this.onTap});
  final String? selectedId;
  final NodeItem? chosen;
  final bool loading;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final n = chosen;
    final (flag, title, subtitle) = selectedId == null
        ? ('', '自动选择', '连接时按本机延迟检测选择可用线路')
        : loading
            ? ('', '已选线路', '正在更新线路状态')
            : n == null
                ? ('', '已选线路', '列表中找不到，请刷新或重新选择')
                : (
                    countryFlag(n.countryCode),
                    n.name,
                    n.available
                        ? [
                            countryName(n.countryCode),
                            if (n.city.isNotEmpty) n.city,
                            if (n.isEgress) egressProtocolLabel(n.protocols.first),
                            if (n.isEgress && n.supportsUdp == false) '不支持 UDP',
                          ].join(' · ')
                        : n.unavailableLabel
                  );
    return Material(
      color: YhColors.paper,
      shape: RoundedRectangleBorder(
          side: const BorderSide(color: YhColors.line),
          borderRadius: BorderRadius.circular(YhRadius.surface)),
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Row(children: [
            Container(
              width: 40,
              height: 40,
              alignment: Alignment.center,
              decoration: BoxDecoration(
                  color: YhColors.soft,
                  borderRadius: BorderRadius.circular(12)),
              child: flag.isEmpty
                  ? const Icon(Icons.bolt, color: YhColors.accent)
                  : Text(flag, style: const TextStyle(fontSize: 22)),
            ),
            const SizedBox(width: 12),
            Expanded(
                child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                  Text(title, style: text.bodyLarge),
                  Text(subtitle, style: text.labelSmall),
                ])),
            const Icon(Icons.chevron_right, color: YhColors.sub),
          ]),
        ),
      ),
    );
  }
}

class _AccountCard extends StatelessWidget {
  const _AccountCard({required this.me});
  final AsyncValue<Map<String, dynamic>> me;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: YhColors.paper,
        borderRadius: BorderRadius.circular(YhRadius.surface),
        border: Border.all(color: YhColors.line),
      ),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        if (me.valueOrNull?['pending_order'] != null)
          Padding(
            padding: const EdgeInsets.only(bottom: 8),
            child: InkWell(
              onTap: () => context.push(Routes.pay.replaceFirst(
                  ':orderNo', me.value!['pending_order']['order_no'])),
              child: Text('有一笔待付款订单 · 继续支付',
                  style: text.labelSmall?.copyWith(color: YhColors.warn)),
            ),
          ),
        me.when(
          data: (m) {
            final ent = m['entitlement'] as Map<String, dynamic>? ?? const {};
            final reason = ent['blocked_reason'];
            final hint = switch (reason) {
              'NO_PLAN' => '还没有套餐 · 去选择',
              'EXPIRED' => '套餐已到期 · 去续费',
              'QUOTA_EXHAUSTED' => '流量已用完 · 买流量包',
              'SUSPENDED' => '账户已停用 · 联系客服',
              _ =>
                '${ent['plan_name'] ?? ''} · 到期 ${(ent['expire_at'] as String?)?.substring(0, 10) ?? '—'}',
            };
            final noPlan = reason == 'NO_PLAN' || reason == 'EXPIRED';
            return Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                InkWell(
                  onTap: reason == null ? null : () => context.push(Routes.plans),
                  child: Text('账户 ${m['account_no']} · $hint',
                      style: text.labelSmall?.copyWith(
                          color: reason == null ? YhColors.sub : YhColors.warn)),
                ),
                if (noPlan)
                  Align(
                    alignment: Alignment.centerLeft,
                    child: TextButton(
                      onPressed: () => context.push(Routes.redeem),
                      child: const Text('兑换码'),
                    ),
                  ),
              ],
            );
          },
          loading: () => Text('正在同步…', style: text.labelSmall),
          error: (_, __) => Text('无法同步账户信息',
              style: text.labelSmall?.copyWith(color: YhColors.danger)),
        ),
      ]),
    );
  }
}
