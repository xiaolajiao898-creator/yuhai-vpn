import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../../core/widgets/tide_ring_button.dart';
import '../../account/data/trial_api.dart';
import '../../account/presentation/support_sheet.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../../platform/vpn/vpn_engine.dart';
import '../../connection/presentation/connection_controller.dart';
import '../data/me_api.dart';
import 'account_summary.dart';
import '../../nodes/data/auto_select.dart';
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
  var _autoTried = false;
  var _claiming = false;

  @override
  void dispose() {
    _tick?.cancel();
    super.dispose();
  }

  String? _blockedReason() {
    final ent = ref.read(meProvider).valueOrNull?['entitlement'];
    if (ent is Map && ent['blocked_reason'] is String) {
      return ent['blocked_reason'] as String;
    }
    return null;
  }

  bool _isAccessBlock(String? code) =>
      code == 'NO_PLAN' || code == 'EXPIRED' || code == 'QUOTA_EXHAUSTED' || code == 'SUSPENDED';

  Future<void> _onPower() async {
    final reason = _blockedReason();
    if (reason == 'NO_PLAN' || reason == 'EXPIRED') {
      await _showAccessSheet(reason!);
      return;
    }
    if (reason == 'SUSPENDED' || reason == 'QUOTA_EXHAUSTED') {
      await _showAccessSheet(reason!);
      return;
    }
    await ref.read(connectionControllerProvider.notifier).toggle();
    if (!mounted) return;
    final failure = ref.read(connectionFailureProvider);
    if (failure != null && _isAccessBlock(failure.code)) {
      await _showAccessSheet(failure.code);
    }
  }

  void _maybeAutoConnect() {
    if (_autoTried) return;
    final prefs = ref.read(connectionPreferencesProvider).valueOrNull;
    if (prefs == null || !prefs.autoConnect) return;
    if (_blockedReason() != null) {
      _autoTried = true;
      return;
    }
    if (ref.read(connectionControllerProvider) != VpnStatus.disconnected) return;
    _autoTried = true;
    Future<void>.microtask(() {
      if (mounted) ref.read(connectionControllerProvider.notifier).toggle();
    });
  }

  Future<void> _claimTrial() async {
    if (_claiming) return;
    setState(() => _claiming = true);
    try {
      final grant = await ref.read(trialApiProvider).claim();
      ref.invalidate(meProvider);
      if (!mounted) return;
      final when = grant.active ? '试用已开通，到 ${_local(grant.expireAt)}' : '试用已经领过了';
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(when)));
    } on ApiException catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(e.message)));
      }
    } catch (_) {
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(const SnackBar(content: Text('没能开通试用，请重试')));
      }
    } finally {
      if (mounted) setState(() => _claiming = false);
    }
  }

  String _local(DateTime value) {
    final local = value.toLocal();
    String two(int n) => n.toString().padLeft(2, '0');
    return '${local.month}月${local.day}日 ${two(local.hour)}:${two(local.minute)}';
  }

  Future<void> _showAccessSheet(String reason) {
    final trial = reason == 'NO_PLAN';
    return showModalBottomSheet<void>(
      context: context,
      showDragHandle: true,
      builder: (sheetContext) {
        final text = Theme.of(sheetContext).textTheme;
        final title = switch (reason) {
          'EXPIRED' => '套餐已到期',
          'QUOTA_EXHAUSTED' => '流量已用完',
          'SUSPENDED' => '账户暂不可用',
          _ => '还没开通',
        };
        final body = switch (reason) {
          'EXPIRED' => '套餐已经到期。续费或兑换之后就能连接。',
          'QUOTA_EXHAUSTED' => '流量用完了。补充之后就能连接。',
          'SUSPENDED' => '账户已停用。请联系客服，换线路解决不了。',
          _ => '新用户可免费试用 24 小时，或选一个套餐继续使用。',
        };
        return SafeArea(
          child: Padding(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
            child: Column(mainAxisSize: MainAxisSize.min, crossAxisAlignment: CrossAxisAlignment.stretch, children: [
              Text(title, style: text.headlineMedium),
              const SizedBox(height: 8),
              Text(body, style: text.bodyMedium?.copyWith(color: YhColors.sub)),
              const SizedBox(height: 16),
              if (trial)
                FilledButton(
                  onPressed: _claiming
                      ? null
                      : () async {
                          Navigator.pop(sheetContext);
                          await _claimTrial();
                        },
                  child: Text(_claiming ? '正在开通…' : '免费试用 24 小时'),
                ),
              if (trial) const SizedBox(height: 8),
              if (reason != 'SUSPENDED')
                OutlinedButton(
                  onPressed: () {
                    Navigator.pop(sheetContext);
                    context.push(Routes.plans);
                  },
                  child: Text(reason == 'QUOTA_EXHAUSTED' ? '买流量包' : '查看套餐'),
                ),
              if (reason == 'NO_PLAN' || reason == 'EXPIRED')
                TextButton(
                  onPressed: () {
                    Navigator.pop(sheetContext);
                    context.push(Routes.redeem);
                  },
                  child: const Text('兑换码'),
                ),
              TextButton(onPressed: () => showSupportSheet(sheetContext), child: const Text('联系客服')),
            ]),
          ),
        );
      },
    );
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
    final held = ref.watch(trafficHeldProvider);
    final me = ref.watch(meProvider);
    final selectedNode = ref.watch(selectedNodeProvider);
    final nodeState = ref.watch(nodesProvider);
    final stats = status == VpnStatus.connected
        ? ref.watch(vpnStatsProvider).valueOrNull
        : null;
    final chosen =
        nodeState.asData?.value.where((n) => n.id == selectedNode).firstOrNull;
    final text = Theme.of(context).textTheme;
    final reduceMotion = ref.watch(reduceConnectionMotionProvider);
    final blocked = me.valueOrNull?['entitlement'] is Map
        ? (me.valueOrNull!['entitlement'] as Map)['blocked_reason'] as String?
        : null;
    final accessBlock = _isAccessBlock(blocked) ||
        (status == VpnStatus.error && _isAccessBlock(failure?.code));
    final visual = accessBlock && status == VpnStatus.error
        ? VpnStatus.disconnected
        : status;
    _syncTicker(visual == VpnStatus.connected && stats != null);
    _maybeAutoConnect();
    final (label, color) = switch (visual) {
      VpnStatus.connected => (demo ? '模拟已连接' : '已连接', YhColors.good),
      VpnStatus.connecting => ('正在连接', YhColors.accent),
      VpnStatus.reconnecting => ('重连中', YhColors.warn),
      VpnStatus.disconnecting => ('正在断开', YhColors.sub),
      VpnStatus.error => ('连接失败', YhColors.danger),
      VpnStatus.disconnected => ('未连接', YhColors.sub),
    };
    final wash = switch (visual) {
      VpnStatus.connected => const Color(0xFF16301C),
      VpnStatus.connecting || VpnStatus.reconnecting => const Color(0xFF1A2414),
      VpnStatus.error => YhColors.bg,
      _ => YhColors.bg,
    };
    return Scaffold(
      backgroundColor: wash,
      appBar: AppBar(title: const Text('宇海VPN'), backgroundColor: Colors.transparent, actions: [
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
                  status: visual,
                  onTap: _onPower,
                ),
              ),
            ),
            const SizedBox(height: 16),
            if (stats != null) ...[
              Row(mainAxisAlignment: MainAxisAlignment.center, children: [
                _Rate(icon: Icons.arrow_downward, value: stats.downSpeed),
                const SizedBox(width: 24),
                _Rate(icon: Icons.arrow_upward, value: stats.upSpeed),
              ]),
              const SizedBox(height: 4),
              Text('本次流量 ${_bytes(stats.downBytes + stats.upBytes)}',
                  textAlign: TextAlign.center,
                  style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric)),
            ],
            if (blocked == 'NO_PLAN')
              Padding(
                padding: const EdgeInsets.only(top: 16),
                child: _AccessBanner(
                  title: '还没开通',
                  body: '新用户可免费试用 24 小时，或选一个套餐继续使用。',
                  action: '免费试用 24 小时',
                  busy: _claiming,
                  onAction: _claimTrial,
                  onPlans: () => context.push(Routes.plans),
                ),
              ),
            if (failure != null && status == VpnStatus.error && !accessBlock)
              _FailureCard(
                failure: failure,
                onRetry: () =>
                    ref.read(connectionControllerProvider.notifier).toggle(),
                onSwitch: () => context.push(Routes.nodes),
                onSupport: () => showSupportSheet(context),
              ),
            if (notice != null &&
                (status == VpnStatus.connected ||
                    status == VpnStatus.connecting ||
                    status == VpnStatus.reconnecting ||
                    (held && status == VpnStatus.error)))
              Padding(
                padding: const EdgeInsets.only(top: 12),
                child: Text(notice,
                    textAlign: TextAlign.center,
                    style: text.labelSmall?.copyWith(color: YhColors.warn)),
              ),
            if (held && status != VpnStatus.disconnected)
              TextButton(
                onPressed: () =>
                    ref.read(connectionControllerProvider.notifier).release(),
                child: const Text('断开并恢复本地网络'),
              ),
            const SizedBox(height: 24),
            _LineCard(
                selectedId: selectedNode,
                chosen: chosen,
                loading: nodeState.isLoading,
                autoHint: _autoHint(nodeState.asData?.value),
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

  static String _autoHint(List<NodeItem>? nodes) {
    if (nodes == null) return '连接时按本机延迟检测选择可用线路';
    final probed = nodes.any((n) =>
        n.available && !n.isEgress && n.probeHost != null && n.probePort != null);
    final egress = nodes.any(
        (n) => canAutoSelect(n, egressSupported: egressPlatformSupported) && n.isEgress);
    if (!probed && egress) return '连接时自动使用可用线路';
    return '连接时按本机延迟检测选择可用线路';
  }

  static String _bytes(int n) {
    const units = ['B', 'KB', 'MB', 'GB'];
    var value = n.toDouble();
    var unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit++;
    }
    final digits = unit == 0 || value >= 10 ? 0 : 1;
    return '${value.toStringAsFixed(digits)} ${units[unit]}';
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

class _AccessBanner extends StatelessWidget {
  const _AccessBanner({
    required this.title,
    required this.body,
    required this.action,
    required this.busy,
    required this.onAction,
    required this.onPlans,
  });
  final String title, body, action;
  final bool busy;
  final VoidCallback onAction, onPlans;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: YhColors.paper,
        borderRadius: BorderRadius.circular(YhRadius.surface),
        border: Border.all(color: YhColors.accent.withValues(alpha: .5)),
      ),
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Text(title, style: text.titleLarge),
        const SizedBox(height: 4),
        Text(body, style: text.bodyMedium?.copyWith(color: YhColors.sub)),
        const SizedBox(height: 12),
        FilledButton(onPressed: busy ? null : onAction, child: Text(busy ? '正在开通…' : action)),
        TextButton(onPressed: onPlans, child: const Text('查看套餐')),
      ]),
    );
  }
}

class _FailureCard extends StatelessWidget {
  const _FailureCard(
      {required this.failure,
      required this.onRetry,
      required this.onSwitch,
      required this.onSupport});
  final VpnFailure failure;
  final VoidCallback onRetry, onSwitch, onSupport;

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
          Text(failure.message,
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
                    onPressed: onSwitch, child: const Text('换线路'))),
          ]),
          TextButton(onPressed: onSupport, child: const Text('联系客服')),
        ]),
      );
}

class _LineCard extends StatelessWidget {
  const _LineCard(
      {required this.selectedId,
      required this.chosen,
      required this.loading,
      required this.autoHint,
      required this.onTap});
  final String? selectedId;
  final NodeItem? chosen;
  final bool loading;
  final String autoHint;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final n = chosen;
    final (flag, title, subtitle) = selectedId == null
        ? ('', '自动选择', autoHint)
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
            final hint = accountSummaryLine(ent);
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
          error: (_, __) => Text('账户信息暂时没同步上',
              style: text.labelSmall?.copyWith(color: YhColors.danger)),
        ),
      ]),
    );
  }
}
