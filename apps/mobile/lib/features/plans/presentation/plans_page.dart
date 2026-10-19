import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/support_config.dart';
import '../../account/presentation/support_sheet.dart';
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
          body: '请检查网络后再试。如果一直这样，可以联系客服。',
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
            return _PlansMessage(
              icon: Icons.inventory_2_outlined,
              title: '还没有上架套餐',
              body: '价格由后台配置。套餐上架之后会出现在这里，不会显示占位价格。你也可以先领 24 小时免费试用，或使用兑换码。',
              primary: '重试',
              onPrimary: () => ref.invalidate(plansProvider),
              secondary: '联系客服',
              onSecondary: () => showSupportSheet(context),
            );
          }
          final recommended = time.cast<PlanItem?>().firstWhere(
                (p) => p!.badge == '推荐',
                orElse: () => null,
              );
          return ListView(
            padding: const EdgeInsets.fromLTRB(YhSpace.page, 8, YhSpace.page, 32),
            children: [
              const _TrialBanner(),
              const SizedBox(height: 16),
              Text('选择时长', style: text.titleLarge),
              const SizedBox(height: 6),
              Text('价格是 USDT。不自动续费，到期前不会扣款。到账后自动开通。',
                  style: text.bodyMedium?.copyWith(color: YhColors.sub)),
              const SizedBox(height: 16),
              if (time.isEmpty)
                Text('当前没有时长套餐。', style: text.bodyMedium)
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
              Text('加密货币付款不可撤销。也可以先充值余额再购买。',
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

class _TrialBanner extends StatelessWidget {
  const _TrialBanner();

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Container(
      padding: const EdgeInsets.all(16),
      decoration: BoxDecoration(
        color: YhColors.accent.withValues(alpha: 0.12),
        borderRadius: BorderRadius.circular(YhRadius.surface),
        border: Border.all(color: YhColors.accent.withValues(alpha: 0.45)),
      ),
      child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
        Text('新用户免费试用 24 小时', style: text.titleLarge?.copyWith(color: YhColors.accent)),
        const SizedBox(height: 6),
        Text('L1 · 不限流量 · 3 台设备 · 不自动续费。每个账户和每台设备只能领一次。',
            style: text.bodyMedium),
        const SizedBox(height: 12),
        FilledButton(
          onPressed: () => context.go(Routes.home),
          child: const Text('回首页领取'),
        ),
      ]),
    );
  }
}

class _PlanCard extends StatelessWidget {
  const _PlanCard({required this.plan, required this.recommended, required this.onTap});
  final PlanItem plan;
  final bool recommended;
  final VoidCallback onTap;

  String get _meta {
    final parts = <String>[];
    if (plan.durationDays != null && plan.type == 'TIME') {
      parts.add(_lengthLabel(plan.durationDays!, plan.name));
    }
    if (plan.type == 'TIME') {
      parts.add(plan.quotaBytes == null ? '不限流量' : '含流量额度');
      if (plan.deviceLimit != null) parts.add('${plan.deviceLimit} 台设备');
    } else if (plan.type == 'TRAFFIC') {
      if (plan.durationDays != null) parts.add('${plan.durationDays} 天内有效');
    } else {
      parts.add('与当前套餐同时到期');
    }
    parts.add('不自动续费');
    return parts.join(' · ');
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
            child: Row(children: [
              Expanded(
                child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
                  Row(children: [
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
                  const SizedBox(height: 6),
                  Text(_meta, style: text.bodyMedium?.copyWith(color: YhColors.sub)),
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
          ),
        ),
      ),
    );
  }
}

String _lengthLabel(int days, String name) {
  if (name.contains('周')) return '周卡';
  if (name.contains('季')) return '季卡';
  if (name.contains('年')) return '年卡';
  if (name.contains('月')) return '月卡';
  if (days <= 7) return '周卡';
  if (days <= 31) return '月卡';
  if (days <= 100) return '季卡';
  return '年卡';
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
          Text(kSupportUrlIsPlaceholder ? '客服地址仍是占位，上线前需要换成真实 Telegram。' : '',
              style: text.labelSmall, textAlign: TextAlign.center),
        ]),
      ),
    );
  }
}
