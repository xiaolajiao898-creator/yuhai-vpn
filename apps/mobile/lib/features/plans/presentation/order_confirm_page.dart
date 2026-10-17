import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/network/api_client.dart';
import '../../home/data/me_api.dart';
import '../../payment/data/orders_api.dart';
import '../data/plans_api.dart';

/// P08 确认订单（方案 9.4）：报价明细（可展开）、到期时间变化、应付金额、支付方式。
/// 报价 10 分钟有效；过期或改价（41001 / 42202）自动重新报价。
class OrderConfirmPage extends ConsumerStatefulWidget {
  const OrderConfirmPage({super.key, required this.planCode});
  final String planCode;

  @override
  ConsumerState<OrderConfirmPage> createState() => _State();
}

class _State extends ConsumerState<OrderConfirmPage> {
  QuoteResult? _quote;
  String _payMethod = 'USDT_TRC20';
  bool _busy = false;
  bool _showLines = false;
  String? _error;

  @override
  void initState() {
    super.initState();
    _loadQuote();
  }

  Future<void> _loadQuote() async {
    setState(() {
      _busy = true;
      _error = null;
    });
    try {
      _quote = await ref.read(plansApiProvider).quote(widget.planCode);
    } on ApiException catch (e) {
      _error = e.message;
    } catch (_) {
      _error = '网络不可用，请稍后再试';
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _submit() async {
    final q = _quote;
    if (q == null) return;
    setState(() => _busy = true);
    try {
      final order = await ref.read(plansApiProvider).createOrder(quoteId: q.quoteId, payMethod: _payMethod);
      ref.invalidate(meProvider);
      if (!mounted) return;
      final no = order['order_no'] as String;
      if (order['status'] == 'COMPLETED') {
        context.go(Routes.payResult.replaceFirst(':orderNo', no), extra: OrderView.fromJson(order));
      } else {
        context.go(Routes.pay.replaceFirst(':orderNo', no), extra: order);
      }
    } on ApiException catch (e) {
      if (e.code == 41001 || e.code == 42202) {
        await _loadQuote();
        if (mounted) setState(() => _error = '报价已更新，请确认后再提交');
      } else if (e.code == 40201) {
        setState(() {
          _error = '余额不足，请改用 USDT 支付或先充值';
          _payMethod = 'USDT_TRC20';
        });
      } else if (e.code == 40906) {
        setState(() => _error = '待支付订单过多，请先在订单记录中完成或取消');
      } else {
        setState(() => _error = e.message);
      }
    } catch (_) {
      setState(() => _error = '网络不可用，请稍后再试');
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final q = _quote;
    final canPayBalance = q != null && _cmp(q.balanceAvailable, q.baseAmount) >= 0;
    return Scaffold(
      appBar: AppBar(title: const Text('确认订单'), backgroundColor: Colors.transparent),
      body: q == null
          ? Center(child: _busy ? const CircularProgressIndicator() : Text(_error ?? '', style: const TextStyle(color: YhColors.coral)))
          : ListView(
              padding: const EdgeInsets.symmetric(horizontal: YhSpace.page, vertical: YhSpace.unit),
              children: [
                _Panel(children: [
                  Text(q.planName, style: text.titleLarge),
                  const SizedBox(height: YhSpace.unit),
                  Text(_changeText(q), style: text.labelSmall),
                  if (q.newExpireAt != null) ...[
                    const SizedBox(height: YhSpace.unit),
                    Text('新的到期时间：${q.newExpireAt!.toLocal().toString().substring(0, 16)}', style: text.bodyMedium),
                  ],
                ]),
                const SizedBox(height: YhSpace.unit * 2),
                _Panel(children: [
                  InkWell(
                    onTap: () => setState(() => _showLines = !_showLines),
                    child: Row(children: [
                      Text('应付金额', style: text.bodyMedium),
                      const Spacer(),
                      Text('${_trim(q.baseAmount)} USDT',
                          style: text.headlineMedium?.copyWith(fontFamily: YhFont.numeric, fontFeatures: const [FontFeature.tabularFigures()])),
                      Icon(_showLines ? Icons.expand_less : Icons.expand_more, color: YhColors.haze),
                    ]),
                  ),
                  if (_showLines)
                    for (final l in q.lines)
                      Padding(
                        padding: const EdgeInsets.only(top: YhSpace.unit),
                        child: Row(children: [
                          Expanded(child: Text(l.label, style: text.labelSmall)),
                          Text(_trim(l.amount), style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric)),
                        ]),
                      ),
                  const SizedBox(height: YhSpace.unit),
                  Text('USDT 付款时会在此基础上加一个唯一尾数用于识别，下一页会显示准确的到账数量。', style: text.labelSmall),
                ]),
                const SizedBox(height: YhSpace.unit * 2),
                Text('支付方式', style: text.labelSmall),
                const SizedBox(height: YhSpace.unit),
                _PayOption(
                  title: 'USDT（TRC20）',
                  subtitle: '链上转账，到账后自动开通',
                  selected: _payMethod == 'USDT_TRC20',
                  onTap: () => setState(() => _payMethod = 'USDT_TRC20'),
                ),
                _PayOption(
                  title: '余额支付',
                  subtitle: '可用余额 ${_trim(q.balanceAvailable)} USDT${canPayBalance ? '' : '（不足）'}',
                  selected: _payMethod == 'BALANCE',
                  enabled: canPayBalance,
                  onTap: () => setState(() => _payMethod = 'BALANCE'),
                ),
                if (_error != null) ...[
                  const SizedBox(height: YhSpace.unit),
                  Text(_error!, style: text.bodyMedium?.copyWith(color: YhColors.coral)),
                ],
                const SizedBox(height: YhSpace.page),
                FilledButton(
                  onPressed: _busy ? null : _submit,
                  child: Text(_payMethod == 'BALANCE' ? '用余额支付并开通' : '生成付款信息'),
                ),
                const SizedBox(height: YhSpace.page),
              ],
            ),
    );
  }

  String _changeText(QuoteResult q) => switch (q.changeType) {
        'RENEW' => '同等级续费，在当前到期时间上顺延',
        'TIER_CHANGE' => '变更等级，当前套餐剩余价值已折算抵扣',
        'ADDON' => '设备加购，按当前套餐剩余天数计价',
        'TRAFFIC_PACK' => '流量包独立计算有效期；持有不限量套餐时不会被消耗',
        _ => '新购套餐，从开通时刻起算',
      };
}

int _cmp(String a, String b) => (double.parse(a) - double.parse(b)).sign.toInt(); // 仅用于 UI 展示比较，服务端才是权威

String _trim(String amount) {
  var s = amount;
  if (s.contains('.')) s = s.replaceAll(RegExp(r'0+$'), '').replaceAll(RegExp(r'\.$'), '');
  return s;
}

class _Panel extends StatelessWidget {
  const _Panel({required this.children});
  final List<Widget> children;
  @override
  Widget build(BuildContext context) => Container(
        width: double.infinity,
        padding: const EdgeInsets.all(YhSpace.panel),
        decoration: BoxDecoration(color: YhColors.current, border: Border.all(color: YhColors.reef), borderRadius: BorderRadius.circular(YhRadius.button)),
        child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: children),
      );
}

class _PayOption extends StatelessWidget {
  const _PayOption({required this.title, required this.subtitle, required this.selected, this.enabled = true, required this.onTap});
  final String title, subtitle;
  final bool selected, enabled;
  final VoidCallback onTap;
  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Opacity(
      opacity: enabled ? 1 : 0.5,
      child: Container(
        margin: const EdgeInsets.only(bottom: YhSpace.unit),
        decoration: BoxDecoration(
          border: Border.all(color: selected ? YhColors.glow : YhColors.reef),
          borderRadius: BorderRadius.circular(YhRadius.listSelected),
        ),
        child: ListTile(
          enabled: enabled,
          onTap: enabled ? onTap : null,
          title: Text(title, style: text.bodyLarge),
          subtitle: Text(subtitle, style: text.labelSmall),
          trailing: Icon(selected ? Icons.radio_button_checked : Icons.radio_button_off, color: selected ? YhColors.glow : YhColors.haze),
        ),
      ),
    );
  }
}
