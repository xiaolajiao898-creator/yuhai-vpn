import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../data/plans_api.dart';

/// P07 套餐页（方案 9.8）：按类型分组，价格用等宽数字；点击进入确认订单页。
class PlansPage extends ConsumerWidget {
  const PlansPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final plans = ref.watch(plansProvider);
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('套餐'), backgroundColor: Colors.transparent),
      body: plans.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => Center(child: Text('无法加载套餐', style: text.bodyMedium?.copyWith(color: YhColors.coral))),
        data: (list) {
          final groups = {
            'TIME': ('时长套餐', list.where((p) => p.type == 'TIME')),
            'TRAFFIC': ('流量包', list.where((p) => p.type == 'TRAFFIC')),
            'DEVICE_ADDON': ('设备加购', list.where((p) => p.type == 'DEVICE_ADDON')),
          };
          return ListView(
            padding: const EdgeInsets.symmetric(horizontal: YhSpace.page, vertical: YhSpace.unit),
            children: [
              for (final g in groups.values)
                if (g.$2.isNotEmpty) ...[
                  Padding(padding: const EdgeInsets.only(top: YhSpace.page, bottom: YhSpace.unit), child: Text(g.$1, style: text.labelSmall)),
                  for (final p in g.$2) _PlanCard(plan: p, onTap: () => context.push(Routes.orderConfirm, extra: p.code)),
                ],
              const SizedBox(height: YhSpace.page),
              Text('加密货币付款不可撤销；到账后自动开通。也可先充值余额再购买。', style: text.labelSmall),
            ],
          );
        },
      ),
    );
  }
}

class _PlanCard extends StatelessWidget {
  const _PlanCard({required this.plan, required this.onTap});
  final PlanItem plan;
  final VoidCallback onTap;

  String get _desc {
    final parts = <String>[];
    if (plan.type == 'TIME') {
      parts.add(plan.quotaBytes == null ? '不限量' : '每 30 天 ${plan.quotaBytes! ~/ (1024 * 1024 * 1024)} GB');
      if (plan.deviceLimit != null) parts.add('${plan.deviceLimit} 台设备');
    } else if (plan.type == 'TRAFFIC') {
      parts.add('${plan.durationDays} 天内有效');
      if (plan.deviceLimit != null) parts.add('${plan.deviceLimit} 台设备');
    } else {
      parts.add('与当前套餐同时到期，按剩余天数计价');
    }
    return parts.join(' · ');
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return InkWell(
      onTap: onTap,
      borderRadius: BorderRadius.circular(YhRadius.button),
      child: Container(
        margin: const EdgeInsets.only(bottom: YhSpace.unit),
        padding: const EdgeInsets.all(YhSpace.panel),
        decoration: BoxDecoration(
          color: YhColors.current,
          border: Border.all(color: plan.badge == '推荐' ? YhColors.glow : YhColors.reef),
          borderRadius: BorderRadius.circular(YhRadius.button),
        ),
        child: Row(
          children: [
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Row(children: [
                    Text(plan.name, style: text.bodyLarge),
                    if (plan.badge != null) ...[
                      const SizedBox(width: YhSpace.unit),
                      Container(
                        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                        decoration: BoxDecoration(color: YhColors.reef, borderRadius: BorderRadius.circular(YhRadius.tag)),
                        child: Text(plan.badge!, style: text.labelSmall?.copyWith(color: YhColors.starlight)),
                      ),
                    ],
                  ]),
                  const SizedBox(height: 4),
                  Text(_desc, style: text.labelSmall),
                ],
              ),
            ),
            Text('${_trim(plan.listPrice)} USDT',
                style: text.titleLarge?.copyWith(fontFamily: YhFont.numeric, fontFeatures: const [FontFeature.tabularFigures()])),
          ],
        ),
      ),
    );
  }
}

String _trim(String amount) {
  var s = amount;
  if (s.contains('.')) {
    s = s.replaceAll(RegExp(r'0+$'), '').replaceAll(RegExp(r'\.$'), '');
  }
  return s;
}
