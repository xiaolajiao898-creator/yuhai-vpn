import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';

import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../../core/widgets/brand.dart';
import '../../../core/widgets/tide_ring_button.dart';
import '../../account/data/trial_api.dart';
import '../../../core/support_config.dart';
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
              if (kSupportAvailable)
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
      VpnStatus.connecting => ('正在连接', YhColors.flow),
      VpnStatus.reconnecting => ('重连中', YhColors.warn),
      VpnStatus.disconnecting => ('正在断开', YhColors.sub),
      VpnStatus.error => ('连接失败', YhColors.danger),
      VpnStatus.disconnected => ('未连接', YhColors.ink),
    };
    final eyebrow = switch (visual) {
      VpnStatus.connected => 'PROTECTED · 已加密',
      VpnStatus.connecting || VpnStatus.reconnecting => 'LINKING · 建立隧道',
      VpnStatus.error => 'ATTENTION · 需要处理',
      _ => 'STATUS · 连接状态',
    };
    return Scaffold(
      backgroundColor: YhColors.bg,
      extendBody: true,
      appBar: AppBar(
        backgroundColor: Colors.transparent,
        titleSpacing: YhSpace.page,
        title: Row(mainAxisSize: MainAxisSize.min, children: [
          const BrandMark(size: 30),
          const SizedBox(width: 12),
          Text('宇海VPN', style: YhType.display(24)),
        ]),
        actions: [
          TextButton(
              onPressed: () => context.push(Routes.balance),
              child: const Text('账单')),
          Padding(
            padding: const EdgeInsets.only(right: 12),
            child: IconButton(
                tooltip: '设置',
                onPressed: () => context.push(Routes.settings),
                style: IconButton.styleFrom(
                    side: const BorderSide(color: YhColors.hairStrong)),
                icon: const Icon(Icons.tune_rounded, size: 20)),
          ),
        ],
      ),
      bottomNavigationBar: YhTabBar(
        index: 0,
        onSelect: (index) {
          if (index == 1) context.push(Routes.plans);
          if (index == 2) context.push(Routes.invite);
          if (index == 3) context.push(Routes.settings);
        },
      ),
      body: TideBackdrop(
        linesTop: 0.30,
        glow: visual == VpnStatus.connected ? 1.4 : 1,
        child: SafeArea(
          bottom: false,
          child: ListView(
            padding: const EdgeInsets.fromLTRB(YhSpace.page, 0, YhSpace.page, 120),
            children: [
              if (demo)
                Padding(
                    padding: const EdgeInsets.only(top: 4),
                    child: Text('演示模式 · 示例数据，不会建立真实 VPN 连接',
                        textAlign: TextAlign.center,
                        style: text.labelSmall?.copyWith(color: YhColors.warn))),
              const SizedBox(height: 14),
              Center(child: Eyebrow(eyebrow, color: color == YhColors.ink ? YhColors.accentInk : color)),
              const SizedBox(height: 10),
              Text(label,
                  textAlign: TextAlign.center,
                  style: YhType.display(44, color: color, height: 1.05)),
              if (stats != null) ...[
                const SizedBox(height: 6),
                Text(_duration(DateTime.now().toUtc().difference(stats.since)),
                    textAlign: TextAlign.center,
                    style: YhType.latin(26, color: YhColors.accentInk, weight: FontWeight.w500, spacing: 1.5)),
              ],
              const SizedBox(height: 10),
              MediaQuery(
                data: MediaQuery.of(context).copyWith(
                    disableAnimations:
                        MediaQuery.of(context).disableAnimations || reduceMotion),
                child: _Stage(
                  ring: (size) => TideRingButton(
                    status: visual,
                    onTap: _onPower,
                    size: size,
                  ),
                ),
              ),
              const SizedBox(height: 16),
              if (stats != null) ...[
                Row(mainAxisAlignment: MainAxisAlignment.center, children: [
                  _Rate(icon: Icons.south_rounded, value: stats.downSpeed),
                  const SizedBox(width: 12),
                  _Rate(icon: Icons.north_rounded, value: stats.upSpeed),
                ]),
                const SizedBox(height: 8),
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
                  onSupport: kSupportAvailable ? () => showSupportSheet(context) : null,
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
              const SizedBox(height: 22),
              _LineCard(
                  selectedId: selectedNode,
                  chosen: chosen,
                  loading: nodeState.isLoading,
                  autoHint: _autoHint(nodeState.asData?.value),
                  onTap: () => context.push(Routes.nodes)),
              const SizedBox(height: 12),
              _AccountCard(me: me),
              const SizedBox(height: 6),
              Align(
                  alignment: Alignment.centerLeft,
                  child: TextButton.icon(
                    onPressed: () => context.push(Routes.traffic),
                    icon: const Icon(Icons.bar_chart_rounded, size: 18),
                    label: const Text('查看流量明细'),
                  )),
            ],
          ),
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
  Widget build(BuildContext context) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 14, vertical: 8),
        decoration: BoxDecoration(
          color: YhColors.paper,
          borderRadius: BorderRadius.circular(YhRadius.tag),
          border: Border.all(color: YhColors.hair),
        ),
        child: Row(mainAxisSize: MainAxisSize.min, children: [
          Icon(icon, size: 14, color: YhColors.accentInk),
          const SizedBox(width: 6),
          Text(format(value), style: YhType.latin(12, color: YhColors.ink, weight: FontWeight.w500, spacing: 0.4)),
        ]),
      );
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
      padding: const EdgeInsets.fromLTRB(22, 22, 22, 14),
      decoration: BoxDecoration(
        color: YhColors.lime,
        borderRadius: BorderRadius.circular(YhRadius.surface),
      ),
      child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
        Row(children: [
          Expanded(child: Text(title, style: YhType.display(28, color: YhColors.buttonInk))),
          Text('24H', style: YhType.latin(22, color: YhColors.buttonInk)),
        ]),
        const SizedBox(height: 6),
        Text(body, style: text.bodyMedium?.copyWith(color: YhColors.buttonInk.withValues(alpha: 0.8))),
        const SizedBox(height: 16),
        FilledButton(
          style: FilledButton.styleFrom(
              backgroundColor: YhColors.buttonInk,
              foregroundColor: YhColors.lime,
              disabledBackgroundColor: YhColors.buttonInk.withValues(alpha: 0.6),
              disabledForegroundColor: YhColors.lime.withValues(alpha: 0.7)),
          onPressed: busy ? null : onAction,
          child: Text(busy ? '正在开通…' : action),
        ),
        TextButton(
          style: TextButton.styleFrom(foregroundColor: YhColors.buttonInk),
          onPressed: onPlans,
          child: const Text('查看套餐'),
        ),
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
  final VoidCallback onRetry, onSwitch;
  final VoidCallback? onSupport;

  @override
  Widget build(BuildContext context) => Container(
        margin: const EdgeInsets.only(top: 14),
        padding: const EdgeInsets.all(18),
        decoration: BoxDecoration(
          color: YhColors.danger.withValues(alpha: 0.08),
          borderRadius: BorderRadius.circular(YhRadius.surface),
          border: Border.all(color: YhColors.danger.withValues(alpha: .45)),
        ),
        child: Column(crossAxisAlignment: CrossAxisAlignment.stretch, children: [
          Text(failure.message,
              style: Theme.of(context)
                  .textTheme
                  .bodyMedium
                  ?.copyWith(color: YhColors.danger)),
          const SizedBox(height: 14),
          Row(children: [
            Expanded(
                child: OutlinedButton(
                    onPressed: onRetry, child: const Text('重试'))),
            const SizedBox(width: 12),
            Expanded(
                child: OutlinedButton(
                    onPressed: onSwitch, child: const Text('换线路'))),
          ]),
          if (onSupport != null) TextButton(onPressed: onSupport, child: const Text('联系客服')),
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
    return YhPanel(
      onTap: onTap,
      padding: const EdgeInsets.fromLTRB(16, 16, 14, 16),
      child: Row(children: [
        Container(
          width: 48,
          height: 48,
          alignment: Alignment.center,
          decoration: BoxDecoration(
              color: flag.isEmpty ? YhColors.lime : YhColors.soft,
              borderRadius: BorderRadius.circular(YhRadius.listSelected)),
          child: flag.isEmpty
              ? const Icon(Icons.bolt_rounded, color: YhColors.buttonInk)
              : Text(flag, style: const TextStyle(fontSize: 24)),
        ),
        const SizedBox(width: 14),
        Expanded(
            child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
              Text('ROUTE · 线路', style: YhType.eyebrow(color: YhColors.faint)),
              const SizedBox(height: 4),
              Text(title, style: YhType.display(21)),
              const SizedBox(height: 2),
              Text(subtitle, style: text.labelSmall),
            ])),
        const SizedBox(width: 8),
        Container(
          width: 34,
          height: 34,
          decoration: BoxDecoration(shape: BoxShape.circle, border: Border.all(color: YhColors.hairStrong)),
          child: const Icon(Icons.arrow_forward_rounded, size: 17, color: YhColors.ink),
        ),
      ]),
    );
  }
}

class _AccountCard extends StatelessWidget {
  const _AccountCard({required this.me});
  final AsyncValue<Map<String, dynamic>> me;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return YhPanel(
      padding: const EdgeInsets.fromLTRB(18, 16, 18, 16),
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
                Row(children: [
                  Text('ACCOUNT · 账户', style: YhType.eyebrow(color: YhColors.faint)),
                  const Spacer(),
                  if (reason == null)
                    Container(
                      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 3),
                      decoration: BoxDecoration(
                          color: YhColors.lime,
                          borderRadius: BorderRadius.circular(YhRadius.tag)),
                      child: Text(planLabel(ent), style: text.labelSmall?.copyWith(color: YhColors.buttonInk, fontWeight: FontWeight.w700)),
                    ),
                ]),
                const SizedBox(height: 8),
                InkWell(
                  onTap: reason == null ? null : () => context.push(Routes.plans),
                  child: Text('账户 ${m['account_no']} · $hint',
                      style: text.labelSmall?.copyWith(
                          color: reason == null ? YhColors.sub : YhColors.warn, height: 1.6)),
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

/// 首页舞台：吉祥物艾丽站在大圆按钮左边，身体挡住一点圆环的左沿，脚下一道影子。
/// 艾丽只是图，点击会穿过去落到按钮上。宽度不够时按钮和艾丽一起缩小，按钮最小 200。
class _Stage extends StatelessWidget {
  const _Stage({required this.ring});
  final Widget Function(double size) ring;

  @override
  Widget build(BuildContext context) => LayoutBuilder(builder: (context, constraints) {
        final width = constraints.maxWidth;
        final size = (width / 1.23).clamp(200.0, 300.0);
        final figureHeight = size * 1.2;
        final figureWidth = figureHeight * AiliFigure.aspect;
        final overlap = figureWidth * 0.3;
        final total = figureWidth - overlap + size;
        final left = ((width - total) / 2).clamp(0.0, double.infinity);
        return SizedBox(
          height: figureHeight + 8,
          child: Stack(clipBehavior: Clip.none, children: [
            // 艾丽脚下的影子
            Positioned(
              left: left - figureWidth * 0.1,
              width: figureWidth * 1.2,
              bottom: 0,
              height: 18,
              child: const DecoratedBox(
                decoration: BoxDecoration(
                  gradient: RadialGradient(colors: [Color(0x335A4A2A), Color(0x005A4A2A)]),
                ),
              ),
            ),
            Positioned(left: left + figureWidth - overlap, bottom: 6, child: ring(size)),
            Positioned(left: left, bottom: 6, child: AiliFigure(height: figureHeight)),
          ]),
        );
      });
}
