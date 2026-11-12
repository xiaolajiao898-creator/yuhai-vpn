import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:qr_flutter/qr_flutter.dart';

import '../../../app/providers.dart';
import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../home/data/me_api.dart';
import '../data/orders_api.dart';

/// P09 USDT 支付页（方案 9.9）：
/// - 地址带 address_proof 时用内置公钥校验；带了证明但校验失败，一律不展示地址
/// - 没有证明的地址（管理员登记，服务端另有启用延迟）照常展示；若本包内置了资金公钥，
///   额外显示提醒：请通过官方渠道核对地址
/// - 二维码只编码地址；金额由用户手动核对，尾数用星辉色突出
/// - 倒计时以 server_time 校准；每 3 秒轮询（If-None-Match），终态停止
class PayPage extends ConsumerStatefulWidget {
  const PayPage({super.key, required this.orderNo, this.initial});
  final String orderNo;
  final Map<String, dynamic>? initial;

  @override
  ConsumerState<PayPage> createState() => _PayPageState();
}

class _PayPageState extends ConsumerState<PayPage> {
  OrderView? _order;
  String? _etag;
  bool? _proofOk;
  bool _signed = false;
  bool _unsigned = false;
  bool _polling = false;
  bool _cancelling = false;
  Timer? _poll, _tick;
  Duration _clockOffset = Duration.zero;
  String? _error;

  @override
  void initState() {
    super.initState();
    if (widget.initial != null) _apply(OrderView.fromJson(widget.initial!));
    _refresh();
    _poll = Timer.periodic(const Duration(seconds: 3), (_) => _refresh());
    _tick = Timer.periodic(const Duration(seconds: 1), (_) => setState(() {}));
  }

  Future<void> _apply(OrderView o) async {
    _clockOffset = o.serverTime.difference(DateTime.now().toUtc());
    final proof = o.addressProof;
    var ok = false;
    var signed = false;
    var unsigned = false;
    if (proof != null) {
      // 带了证明就必须校验通过；失败（含地址/网络缺失）绝不展示地址。
      if (o.receiveAddress != null && o.network != null) {
        ok = await proof.verify(network: o.network!, address: o.receiveAddress!, pubKeys: fundAddressPubKeys);
      }
      signed = ok;
    } else if (o.receiveAddress != null) {
      // 无证明的地址照常展示；本包配置了资金公钥时提醒用户另行核对。
      ok = true;
      unsigned = fundAddressPubKeys.values.any((k) => k.isNotEmpty);
    }
    if (!mounted) return;
    setState(() {
      _order = o;
      _proofOk = ok;
      _signed = signed;
      _unsigned = unsigned;
    });
    if (o.isFinal) {
      _poll?.cancel();
      if (o.status == 'COMPLETED') context.go(Routes.payResult.replaceFirst(':orderNo', o.orderNo), extra: o);
    }
  }

  Future<void> _refresh() async {
    // 3 秒一拍：上一次请求还没回来就跳过这一拍，避免慢网络下请求堆积、乱序覆盖。
    if (_polling) return;
    _polling = true;
    try {
      final (o, tag) = await ref.read(ordersApiProvider).poll(widget.orderNo, etag: _etag);
      _etag = tag;
      if (o != null) {
        await _apply(o);
        if (o.status == 'COMPLETED') ref.invalidate(meProvider);
      }
    } catch (_) {
      // 轮询失败静默重试
    } finally {
      _polling = false;
    }
  }

  Future<void> _cancel() async {
    // 进行中（含确认对话框打开期间）不再响应：同一时刻只有一个对话框、一个取消请求。
    if (_cancelling) return;
    setState(() {
      _cancelling = true;
      _error = null;
    });
    try {
      await _cancelOnce();
    } finally {
      if (mounted) setState(() => _cancelling = false);
    }
  }

  Future<void> _cancelOnce() async {
    final yes = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('取消订单？'),
        content: const Text('如果你已经转账，请不要取消；到账后会自动开通。'),
        actions: [
          TextButton(onPressed: () => Navigator.pop(ctx, false), child: const Text('返回')),
          TextButton(onPressed: () => Navigator.pop(ctx, true), child: const Text('确认取消', style: TextStyle(color: YhColors.coral))),
        ],
      ),
    );
    if (yes != true || !mounted) return;
    try {
      await _apply(await ref.read(ordersApiProvider).cancel(widget.orderNo));
      if (mounted) context.pop();
    } catch (_) {
      if (mounted) setState(() => _error = '取消失败，请刷新后重试');
    }
  }

  @override
  void dispose() {
    _poll?.cancel();
    _tick?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final o = _order;
    final text = Theme.of(context).textTheme;
    if (o == null) return const Scaffold(body: Center(child: CircularProgressIndicator()));
    final now = DateTime.now().toUtc().add(_clockOffset);
    final remain = o.expiresAt.difference(now);
    final (whole, tail) = _splitTail(o.payAmount);
    return Scaffold(
      appBar: AppBar(title: const Text('USDT 付款'), backgroundColor: Colors.transparent),
      body: ListView(
        padding: const EdgeInsets.symmetric(horizontal: YhSpace.page, vertical: YhSpace.unit),
        children: [
          _StatusBanner(order: o, remain: remain),
          const SizedBox(height: YhSpace.unit * 2),
          _Panel(children: [
            Text('到账数量必须等于', style: text.labelSmall),
            const SizedBox(height: 4),
            RichText(
              text: TextSpan(
                style: text.displayLarge?.copyWith(fontFamily: YhFont.numeric, fontFeatures: const [FontFeature.tabularFigures()], fontSize: 36),
                children: [
                  TextSpan(text: whole),
                  TextSpan(text: tail, style: const TextStyle(color: YhColors.starlight)),
                  TextSpan(text: '  USDT', style: text.bodyMedium?.copyWith(color: YhColors.haze)),
                ],
              ),
            ),
            const SizedBox(height: YhSpace.unit),
            Text('尾数用于识别你的付款，请勿四舍五入。从交易所提币时，"到账数量"须等于此值（手续费另计）。', style: text.labelSmall),
            const SizedBox(height: YhSpace.unit),
            OutlinedButton.icon(
              onPressed: () => _copy(context, _trimAmount(o.payAmount), '已复制金额'),
              icon: const Icon(Icons.copy, size: 16),
              label: const Text('复制金额'),
            ),
          ]),
          const SizedBox(height: YhSpace.unit * 2),
          _Panel(children: [
            Row(children: [
              Text('收款地址 · ${o.network == 'MOCK' ? '模拟网络' : 'TRON（TRC20）'}', style: text.labelSmall),
              const Spacer(),
              if (_signed) const Icon(Icons.verified, color: YhColors.glow, size: 16),
            ]),
            const SizedBox(height: YhSpace.unit),
            if (_proofOk == true && o.receiveAddress != null) ...[
              Center(
                child: Container(
                  padding: const EdgeInsets.all(12),
                  // 二维码一律白底黑点，扫码最稳。
                  decoration: BoxDecoration(
                      color: Colors.white,
                      border: Border.all(color: YhColors.hair),
                      borderRadius: BorderRadius.circular(YhRadius.listSelected)),
                  child: QrImageView(data: o.receiveAddress!, size: 180, backgroundColor: Colors.white),
                ),
              ),
              const SizedBox(height: YhSpace.unit * 2),
              SelectableText(o.receiveAddress!, style: text.bodyMedium?.copyWith(fontFamily: YhFont.numeric)),
              const SizedBox(height: YhSpace.unit),
              OutlinedButton.icon(
                onPressed: () => _copy(context, o.receiveAddress!, '已复制地址'),
                icon: const Icon(Icons.copy, size: 16),
                label: const Text('复制地址'),
              ),
              const SizedBox(height: YhSpace.unit),
              Text('仅支持 TRON 网络的 USDT（TRC20）。转错网络无法找回。', style: text.labelSmall?.copyWith(color: YhColors.coral)),
              if (_unsigned) ...[
                const SizedBox(height: YhSpace.unit),
                Text('此收款地址没有附带签名证明。付款前请通过官方渠道（官网或官方客服）核对地址，确认一致后再转账。',
                    style: text.labelSmall?.copyWith(color: YhColors.starlight)),
              ],
            ] else
              Text(_proofOk == null ? '正在校验支付通道…' : '收款地址签名校验失败，为安全起见不展示地址。请不要向任何地址转账，更新 App 或联系客服。',
                  style: text.bodyMedium?.copyWith(color: _proofOk == null ? YhColors.haze : YhColors.coral)),
          ]),
          const SizedBox(height: YhSpace.unit * 2),
          Text('订单号 ${o.orderNo}', style: text.labelSmall?.copyWith(fontFamily: YhFont.numeric)),
          if (_error != null) Text(_error!, style: text.bodyMedium?.copyWith(color: YhColors.coral)),
          const SizedBox(height: YhSpace.page),
          if (o.status == 'PENDING')
            TextButton(
              onPressed: _cancelling ? null : _cancel,
              child: Text(_cancelling ? '正在取消…' : '取消订单', style: const TextStyle(color: YhColors.haze)),
            ),
          const SizedBox(height: YhSpace.page),
        ],
      ),
    );
  }

  Future<void> _copy(BuildContext context, String value, String toast) async {
    await Clipboard.setData(ClipboardData(text: value));
    HapticFeedback.lightImpact();
    if (context.mounted) ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(toast)));
  }
}

/// "6.010000" → ("6", ".01")：突出 0.01 步进的尾数。
(String, String) _splitTail(String amount) {
  final t = _trimAmount(amount);
  final dot = t.indexOf('.');
  if (dot < 0) return (t, '');
  var frac = t.substring(dot + 1);
  if (frac.length < 2) frac = frac.padRight(2, '0');
  return (t.substring(0, dot), '.${frac.substring(0, 2)}');
}

String _trimAmount(String amount) {
  var s = amount;
  if (s.contains('.')) s = s.replaceAll(RegExp(r'0+$'), '').replaceAll(RegExp(r'\.$'), '');
  if (!s.contains('.')) return '$s.00';
  if (s.length - s.indexOf('.') - 1 < 2) s = s.padRight(s.indexOf('.') + 3, '0');
  return s;
}

class _StatusBanner extends StatelessWidget {
  const _StatusBanner({required this.order, required this.remain});
  final OrderView order;
  final Duration remain;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final (label, color) = switch (order.status) {
      'PENDING' when order.statusHint == 'EXCEPTION_REVIEW' => ('发现一笔金额不符的转账，已转人工处理，请保留交易哈希并联系客服', YhColors.starlight),
      'PENDING' => remain.isNegative ? ('订单已过期', YhColors.coral) : ('等待付款 · 剩余 ${_fmt(remain)}', YhColors.moonlight),
      'CONFIRMING' => ('已检测到转账，等待区块确认…', YhColors.starlight),
      'PAID' => ('付款已确认，正在开通…', YhColors.glow),
      'COMPLETED' => ('已开通', YhColors.glow),
      'EXPIRED' => ('订单已过期。若已转账，到账后仍会自动处理', YhColors.coral),
      'CANCELLED' => ('订单已取消', YhColors.haze),
      _ => (order.status, YhColors.haze),
    };
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: YhSpace.panel, vertical: 12),
      decoration: BoxDecoration(border: Border.all(color: color), borderRadius: BorderRadius.circular(YhRadius.listSelected)),
      child: Text(label, style: text.bodyMedium?.copyWith(color: color, fontFamily: YhFont.numeric)),
    );
  }

  String _fmt(Duration d) => '${d.inMinutes.toString().padLeft(2, '0')}:${(d.inSeconds % 60).toString().padLeft(2, '0')}';
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
