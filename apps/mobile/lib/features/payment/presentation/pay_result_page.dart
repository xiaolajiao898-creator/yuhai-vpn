import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../home/data/me_api.dart';
import '../../home/presentation/account_summary.dart';
import '../data/orders_api.dart';

/// P10 支付结果：开通成功、新的到期时间、"开始连接"。荧色只在这里与已连接态使用。
class PayResultPage extends ConsumerWidget {
  const PayResultPage({super.key, required this.orderNo, this.order});
  final String orderNo;
  final OrderView? order;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final me = ref.watch(meProvider);
    final text = Theme.of(context).textTheme;
    final credited = order?.fulfillmentResult == 'CREDITED_TO_BALANCE';
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: YhSpace.page),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              const Spacer(),
              Icon(credited ? Icons.account_balance_wallet_outlined : Icons.check_circle_outline, color: YhColors.glow, size: 56),
              const SizedBox(height: YhSpace.page),
              Text(credited ? '款项已存入余额' : '已开通', style: text.headlineMedium?.copyWith(color: YhColors.glow)),
              const SizedBox(height: YhSpace.unit),
              if (credited)
                Text('套餐当时无法开通（例如主套餐已到期），金额已转入余额，可随时用余额重新购买。', style: text.bodyMedium?.copyWith(color: YhColors.haze))
              else
                me.when(
                  data: (m) {
                    final ent = m['entitlement'] as Map<String, dynamic>? ?? const {};
                    final exp = formatExpiryLocal(ent['expire_at'] as String?);
                    return Text(exp != '—' ? '${planLabel(ent)} · 到期 $exp' : '${order?.planName ?? ''} 已生效', style: text.bodyMedium?.copyWith(color: YhColors.haze));
                  },
                  loading: () => Text('正在同步…', style: text.labelSmall),
                  error: (_, __) => const SizedBox.shrink(),
                ),
              const SizedBox(height: YhSpace.unit),
              Text('订单号 $orderNo', style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric)),
              const Spacer(),
              FilledButton(onPressed: () => context.go(Routes.home), child: Text(credited ? '返回首页' : '开始连接')),
              const SizedBox(height: YhSpace.page),
            ],
          ),
        ),
      ),
    );
  }
}
