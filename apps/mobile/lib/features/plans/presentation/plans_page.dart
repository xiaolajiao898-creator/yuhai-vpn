import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../../core/support_config.dart';
import '../../account/data/trial_api.dart';
import '../../account/presentation/support_sheet.dart';
import '../../home/data/me_api.dart';
import '../data/plans_api.dart';

/// 套餐页：时长卡来自后台，价格不在客户端写死。
class PlansPage extends ConsumerWidget {
  const PlansPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final plans = ref.watch(plansProvider);
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('套餐'), backgroundColor: Colors.transparent),
      body: plans.when(
        loading: () => const _PlansSkeleton(),
        error: (_, __) => _PlansMessage(
          icon: Icons.cloud_off_outlined,
          title: '套餐没有加载出来',
          body: '网络不太好。过一会儿再试，也可以联系客服。',
          primary: '重试',
          onPrimary: () => ref.invalidate(plansProvider),
          secondary: '联系客服',
          onSecondary: () => showSupportSheet(context),
        ),
        data: (list) {
          final time = list.where((p) => p.type == 'TIME').toList()
            ..sort((a, b) => (a.durationDays ?? 0).compareTo(b.durationDays ?? 0));
          final others = list.where((p) => p.type != 'TIME').toList();
          if (list.isEmpty) {
            return Column(children: [
              const Padding(
                padding: EdgeInsets.fromLTRB(YhSpace.page, 8, YhSpace.page, 0),
                child: _TrialOffer(),
              ),
              Expanded(
                child: _PlansMessage(
                  icon: Icons.inventory_2_outlined,
                  title: '还没有可购买的套餐',
                  body: '价格公布后会出现在这里。也可以使用兑换码。',
                  primary: '重试',
                  onPrimary: () => ref.invalidate(plansProvider),
                  secondary: '联系客服',
                  onSecondary: () => showSupportSheet(context),
                ),
              ),
            ]);
          }
          final recommended = time.cast<PlanItem?>().firstWhere(
                (p) => p!.badge == '推荐',
                orElse: () => null,
              );
          return ListView(
            padding: const EdgeInsets.fromLTRB(YhSpace.page, 8, YhSpace.page, 32),
            children: [
              const _TrialOffer(),
              Text('选择时长', style: text.titleLarge),
              const SizedBox(height: 6),
              Text('用 USDT 付款。不自动续费，到期即停。到账后自动开通。',
                  style: text.bodyMedium?.copyWith(color: YhColors.sub)),
              const SizedBox(height: 16),
              if (time.isEmpty)
                Text('现在没有按时长卖的套餐。', style: text.bodyMedium)
              else
                for (final plan in time)
                  _PlanCard(
                    plan: plan,
                    recommended: identical(plan, recommended) || plan.badge == '推荐',
                    onTap: () => context.push(Routes.orderConfirm, extra: plan.code),
                  ),
              if (others.isNotEmpty) ...[
                const SizedBox(height: 8),
                Text('其他', style: text.labelSmall),
                const SizedBox(height: 8),
                for (final plan in others)
                  _PlanCard(
                    plan: plan,
                    recommended: plan.badge == '推荐',
                    onTap: () => context.push(Routes.orderConfirm, extra: plan.code),
                  ),
              ],
              const SizedBox(height: 8),
              Text('链上转账不能撤回。也可以先充进余额再购买。',
                  style: text.labelSmall),
              const SizedBox(height: 12),
              Align(
                alignment: Alignment.centerLeft,
                child: TextButton(
                  onPressed: () => context.push(Routes.redeem),
                  child: const Text('有兑换码'),
                ),
              ),
            ],
          );
        },
      ),
    );
  }
}

class _TrialOffer extends ConsumerStatefulWidget {
  const _TrialOffer();

  @override
  ConsumerState<_TrialOffer> createState() => _TrialOfferState();
}

class _TrialOfferState extends ConsumerState<_TrialOffer> {
  TrialGrant? _grant;
  var _claiming = false;
  String? _error;

  bool _noPlan(AsyncValue<Map<String, dynamic>> me) {
    final ent = me.valueOrNull?['entitlement'];
    if (ent is! Map) return false;
    return ent['blocked_reason'] == 'NO_PLAN';
  }

  Future<void> _claim() async {
    if (_claiming) return;
    setState(() {
      _claiming = true;
      _error = null;
    });
    try {
      final grant = await ref.read(trialApiProvider).claim();
      ref.invalidate(meProvider);
      ref.invalidate(trialStatusProvider);
      if (!mounted) return;
      setState(() => _grant = grant);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } catch (_) {
      if (mounted) setState(() => _error = '没能开通试用，请重试');
    } finally {
      if (mounted) setState(() => _claiming = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final me = ref.watch(meProvider);
    if (!_noPlan(me)) return const SizedBox.shrink();
    if (_grant != null) {
      final when = _expiry(_grant!.expireAt);
      final active = _grant!.active;
      return _TrialCard(
        title: active ? '试用已开通' : '试用已经结束',
        body: active
            ? '到 $when 结束。不限流量，${_grant!.deviceLimit} 台设备。'
            : '到期时间是 $when，不会再延长。',
      );
    }
    final status = ref.watch(trialStatusProvider);
    if (status.isLoading) {
      return const Padding(
        padding: EdgeInsets.only(bottom: 16),
        child: _Bone(height: 112),
      );
    }
    final known = status.asData?.value;
    if (known != null && !known.eligible) {
      if (known.reason == 'DISABLED') {
        return Padding(
          padding: const EdgeInsets.only(bottom: 16),
          child: Text('免费试用暂时关闭。',
              style: Theme.of(context).textTheme.bodyMedium?.copyWith(color: YhColors.sub)),
        );
      }
      return const SizedBox.shrink();
    }
    return _TrialCard(
      title: '新用户免费试用 24 小时',
      body: '不限流量，最多 3 台设备。每个账户和每台设备只能领一次。',
      error: _error,
      action: FilledButton(
        onPressed: _claiming ? null : _claim,
        child: Text(_claiming ? '正在开通…' : '免费试用 24 小时'),
      ),
    );
  }
}

class _TrialCard extends StatelessWidget {
  const _TrialCard({required this.title, required this.body, this.action, this.error});
  final String title, body;
  final Widget? action;
  final String? error;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Container(
        width: double.infinity,
        padding: const EdgeInsets.all(16),
        decoration: BoxDecoration(
          color: YhColors.accent.withValues(alpha: 0.12),
          borderRadius: BorderRadius.circular(YhRadius.surface),
          border: Border.all(color: YhColors.accent.withValues(alpha: 0.45)),
        ),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
          Text(title, style: text.titleLarge?.copyWith(color: YhColors.accent)),
          const SizedBox(height: 6),
          Text(body, style: text.bodyMedium),
          if (error != null) ...[
            const SizedBox(height: 8),
            Text(error!, style: text.bodyMedium?.copyWith(color: YhColors.danger)),
          ],
          if (action != null) ...[
            const SizedBox(height: 12),
            action!,
          ],
        ]),
      ),
    );
  }
}

String _expiry(DateTime value) {
  final local = value.toLocal();
  String two(int n) => n.toString().padLeft(2, '0');
  return '${local.month}月${local.day}日 ${two(local.hour)}:${two(local.minute)}';
}

class _PlanCard extends StatelessWidget {
  const _PlanCard({required this.plan, required this.recommended, required this.onTap});
  final PlanItem plan;
  final bool recommended;
  final VoidCallback onTap;

  String _span(PlanItem plan) {
    final days = plan.durationDays;
    if (days == 90 && !plan.name.contains('季卡')) return '季卡 · 90 天';
    return '$days 天';
  }

  String get _meta {
    if (plan.type == 'TIME') {
      return [
        if (plan.durationDays != null) _span(plan),
        plan.quotaBytes == null ? '不限流量' : '含流量额度',
        if (plan.deviceLimit != null) '${plan.deviceLimit} 台设备',
      ].join(' · ');
    }
    if (plan.type == 'TRAFFIC') {
      return plan.durationDays == null ? '流量包' : '${plan.durationDays} 天内有效';
    }
    return '和当前套餐一起到期';
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final border = recommended ? YhColors.accent : YhColors.line;
    return Padding(
      padding: const EdgeInsets.only(bottom: 12),
      child: Material(
        color: recommended ? YhColors.accent.withValues(alpha: 0.08) : YhColors.paper,
        shape: RoundedRectangleBorder(
          side: BorderSide(color: border, width: recommended ? 1.5 : 1),
          borderRadius: BorderRadius.circular(YhRadius.surface),
        ),
        clipBehavior: Clip.antiAlias,
        child: InkWell(
          onTap: onTap,
          child: Padding(
            padding: const EdgeInsets.all(16),
            child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
              Row(crossAxisAlignment: CrossAxisAlignment.start, children: [
                Expanded(
                  child: Row(children: [
                    Flexible(child: Text(plan.name, style: text.titleLarge)),
                    if (recommended) ...[
                      const SizedBox(width: 8),
                      Container(
                        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
                        decoration: BoxDecoration(
                          color: YhColors.accent,
                          borderRadius: BorderRadius.circular(YhRadius.tag),
                        ),
                        child: Text(plan.badge ?? '推荐',
                            style: text.labelSmall?.copyWith(color: YhColors.buttonInk, fontWeight: FontWeight.w700)),
                      ),
                    ] else if (plan.badge != null) ...[
                      const SizedBox(width: 8),
                      Text(plan.badge!, style: text.labelSmall?.copyWith(color: YhColors.accent)),
                    ],
                  ]),
                ),
                const SizedBox(width: 12),
                Column(crossAxisAlignment: CrossAxisAlignment.end, children: [
                  Text(plan.listPrice,
                      style: text.headlineMedium?.copyWith(
                          fontFeatures: const [FontFeature.tabularFigures()])),
                  Text('USDT', style: text.labelSmall),
                ]),
              ]),
              const SizedBox(height: 6),
              Text(_meta, style: text.bodyMedium?.copyWith(color: YhColors.sub)),
            ]),
          ),
        ),
      ),
    );
  }
}

class _PlansSkeleton extends StatelessWidget {
  const _PlansSkeleton();

  @override
  Widget build(BuildContext context) {
    return ListView(
      padding: const EdgeInsets.all(YhSpace.page),
      children: [
        const _Bone(height: 92),
        const SizedBox(height: 16),
        for (var i = 0; i < 3; i++) ...[
          const _Bone(height: 88),
          const SizedBox(height: 12),
        ],
      ],
    );
  }
}

class _Bone extends StatelessWidget {
  const _Bone({required this.height});
  final double height;
  @override
  Widget build(BuildContext context) => Container(
        height: height,
        decoration: BoxDecoration(
          color: YhColors.soft,
          borderRadius: BorderRadius.circular(YhRadius.surface),
        ),
      );
}

class _PlansMessage extends StatelessWidget {
  const _PlansMessage({
    required this.icon,
    required this.title,
    required this.body,
    required this.primary,
    required this.onPrimary,
    required this.secondary,
    required this.onSecondary,
  });
  final IconData icon;
  final String title, body, primary, secondary;
  final VoidCallback onPrimary, onSecondary;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return SafeArea(
      child: Padding(
        padding: const EdgeInsets.all(YhSpace.page),
        child: Column(children: [
          const Spacer(),
          Icon(icon, size: 48, color: YhColors.accent),
          const SizedBox(height: 16),
          Text(title, style: text.headlineMedium, textAlign: TextAlign.center),
          const SizedBox(height: 8),
          Text(body, style: text.bodyMedium?.copyWith(color: YhColors.sub), textAlign: TextAlign.center),
          const SizedBox(height: 24),
          SizedBox(width: double.infinity, child: FilledButton(onPressed: onPrimary, child: Text(primary))),
          const SizedBox(height: 8),
          SizedBox(width: double.infinity, child: OutlinedButton(onPressed: onSecondary, child: Text(secondary))),
          const Spacer(),
          Text(kSupportUrlIsPlaceholder ? '客服链接还没换成正式账号。' : '',
              style: text.labelSmall, textAlign: TextAlign.center),
        ]),
      ),
    );
  }
}
