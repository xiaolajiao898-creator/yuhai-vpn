import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../../../app/providers.dart';

class PlanItem {
  const PlanItem({required this.code, required this.name, required this.type, required this.tier, required this.listPrice, this.durationDays, this.quotaBytes, this.deviceLimit, this.badge});
  final String code, name, type, listPrice;
  final int tier;
  final int? durationDays, deviceLimit;
  final int? quotaBytes;
  final String? badge;
  static PlanItem fromJson(Map<String, dynamic> j) => PlanItem(
      code: j['code'], name: j['name'], type: j['type'], tier: j['tier'], listPrice: j['list_price'],
      durationDays: j['duration_days'], quotaBytes: j['quota_bytes'], deviceLimit: j['device_limit'], badge: j['badge']);
}

final plansProvider = FutureProvider.autoDispose<List<PlanItem>>((ref) async {
  final d = await ref.watch(apiClientProvider).call('GET', '/v1/plans');
  return ((d['plans'] as List?) ?? const []).cast<Map<String, dynamic>>().map(PlanItem.fromJson).toList();
});

class QuoteLine {
  const QuoteLine(this.label, this.amount);
  final String label, amount;
}

class QuoteResult {
  const QuoteResult({required this.quoteId, required this.planName, required this.changeType, required this.baseAmount, required this.lines, this.newExpireAt, required this.balanceAvailable, required this.expiresAt});
  final String quoteId, planName, changeType, baseAmount, balanceAvailable;
  final List<QuoteLine> lines;
  final DateTime? newExpireAt;
  final DateTime expiresAt;
  static QuoteResult fromJson(Map<String, dynamic> j) => QuoteResult(
        quoteId: j['quote_id'], planName: j['plan']['name'], changeType: j['change_type'], baseAmount: j['base_amount'],
        lines: (j['lines'] as List).map((l) => QuoteLine(l['label'], l['amount'])).toList(),
        newExpireAt: j['new_expire_at'] == null ? null : DateTime.parse(j['new_expire_at']),
        balanceAvailable: j['balance_available'], expiresAt: DateTime.parse(j['expires_at']),
      );
}

class PlansApi {
  PlansApi(this.ref);
  final Ref ref;

  Future<QuoteResult> quote(String planCode) async =>
      QuoteResult.fromJson(await ref.read(apiClientProvider).call('POST', '/v1/plans/quote', json: {'plan_code': planCode}, sign: true));

  /// 同一报价（+支付方式）的幂等键：第一次提交时生成，之后超时/失败重试都沿用同一个，
  /// 服务端据此把重试识别为同一笔下单，不会重复建单。成功后才丢弃。
  final Map<String, String> _orderKeys = {};

  String _orderKey(String quoteId, String payMethod) {
    final k = '$quoteId\n$payMethod';
    final existing = _orderKeys[k];
    if (existing != null) return existing;
    // 报价有效期很短；只保留最近的若干个，避免无界增长。
    if (_orderKeys.length >= 32) _orderKeys.remove(_orderKeys.keys.first);
    return _orderKeys[k] = const Uuid().v4();
  }

  /// 创建订单；返回订单原始 JSON（支付页在第 5–6 周消费）。
  Future<Map<String, dynamic>> createOrder({required String quoteId, required String payMethod}) async {
    final k = '$quoteId\n$payMethod';
    final res = await ref.read(apiClientProvider).call('POST', '/v1/orders',
        json: {'order_type': 'PLAN', 'quote_id': quoteId, 'pay_method': payMethod}, sign: true, idempotencyKey: _orderKey(quoteId, payMethod));
    _orderKeys.remove(k);
    return res;
  }
}

final plansApiProvider = Provider<PlansApi>((ref) => PlansApi(ref));
