import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../data/orders_api.dart';

/// P11 订单记录：列表；待支付点击进入支付页；已完成显示交易哈希。
class OrdersPage extends ConsumerWidget {
  const OrdersPage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final orders = ref.watch(ordersListProvider);
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('订单记录'), backgroundColor: Colors.transparent),
      body: orders.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (_, __) => Center(child: Text('无法加载订单', style: text.bodyMedium?.copyWith(color: YhColors.coral))),
        data: (list) => list.isEmpty
            ? Center(child: Text('还没有订单', style: text.labelSmall))
            : RefreshIndicator(
                onRefresh: () => ref.refresh(ordersListProvider.future),
                child: ListView.separated(
                  padding: const EdgeInsets.symmetric(horizontal: YhSpace.page, vertical: YhSpace.unit),
                  itemCount: list.length,
                  separatorBuilder: (_, __) => const Divider(height: 1),
                  itemBuilder: (_, i) {
                    final o = list[i];
                    final (label, color) = _status(o);
                    return ListTile(
                      contentPadding: EdgeInsets.zero,
                      onTap: o.status == 'PENDING' || o.status == 'CONFIRMING'
                          ? () => context.push(Routes.pay.replaceFirst(':orderNo', o.orderNo))
                          : null,
                      title: Text(o.orderType == 'TOPUP' ? '充值余额' : (o.planName ?? '套餐'), style: text.bodyLarge),
                      subtitle: Text(
                        [o.createdAt.toLocal().toString().substring(0, 16), if (o.txHash != null) '交易 ${o.txHash!.substring(0, 8)}…'].join(' · '),
                        style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric),
                      ),
                      trailing: Column(
                        crossAxisAlignment: CrossAxisAlignment.end,
                        mainAxisAlignment: MainAxisAlignment.center,
                        children: [
                          Text('${_trim(o.payAmount)} USDT', style: text.bodyMedium?.copyWith(fontFamily: YhFont.numeric)),
                          Text(label, style: text.labelSmall?.copyWith(color: color)),
                        ],
                      ),
                    );
                  },
                ),
              ),
      ),
    );
  }

  (String, Color) _status(OrderView o) => switch (o.status) {
        'PENDING' => o.statusHint == 'EXCEPTION_REVIEW' ? ('人工处理中', YhColors.starlight) : ('待付款', YhColors.starlight),
        'CONFIRMING' => ('确认中', YhColors.starlight),
        'PAID' => ('开通中', YhColors.glow),
        'COMPLETED' => (o.fulfillmentResult == 'CREDITED_TO_BALANCE' ? '已入余额' : '已完成', YhColors.glow),
        'EXPIRED' => ('已过期', YhColors.haze),
        'CANCELLED' => ('已取消', YhColors.haze),
        _ => (o.status, YhColors.haze),
      };
}

String _trim(String amount) {
  var s = amount;
  if (s.contains('.')) s = s.replaceAll(RegExp(r'0+$'), '').replaceAll(RegExp(r'\.$'), '');
  return s;
}
