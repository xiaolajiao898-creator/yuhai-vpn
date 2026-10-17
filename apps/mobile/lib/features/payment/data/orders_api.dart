import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../../../app/providers.dart';
import '../../../core/security/address_proof.dart';

class OrderView {
  const OrderView({
    required this.orderNo, required this.orderType, required this.status, this.statusHint, required this.payMethod, this.planName,
    required this.baseAmount, required this.payAmount, this.network, this.receiveAddress, this.addressProof, this.txHash,
    this.fulfillmentResult, required this.expiresAt, this.completedAt, required this.createdAt, required this.serverTime,
  });
  final String orderNo, orderType, status, payMethod, baseAmount, payAmount;
  final String? statusHint, planName, network, receiveAddress, txHash, fulfillmentResult;
  final AddressProof? addressProof;
  final DateTime expiresAt, createdAt, serverTime;
  final DateTime? completedAt;

  bool get isFinal => const {'COMPLETED', 'EXPIRED', 'CANCELLED'}.contains(status);

  static OrderView fromJson(Map<String, dynamic> j) => OrderView(
        orderNo: j['order_no'], orderType: j['order_type'], status: j['status'], statusHint: j['status_hint'], payMethod: j['pay_method'],
        planName: j['plan_name'], baseAmount: j['base_amount'], payAmount: j['pay_amount'], network: j['network'],
        receiveAddress: j['receive_address'], addressProof: AddressProof.fromJson((j['address_proof'] as Map?)?.cast<String, dynamic>()),
        txHash: (j['chain'] as Map?)?['tx_hash'], fulfillmentResult: j['fulfillment_result'],
        expiresAt: DateTime.parse(j['expires_at']), completedAt: j['completed_at'] == null ? null : DateTime.parse(j['completed_at']),
        createdAt: DateTime.parse(j['created_at'] ?? j['expires_at']), serverTime: DateTime.parse(j['server_time']),
      );
}

class OrdersApi {
  OrdersApi(this.ref);
  final Ref ref;

  Future<(OrderView?, String?)> poll(String orderNo, {String? etag}) async {
    final (d, tag) = await ref.read(apiClientProvider).getIfChanged('/v1/orders/$orderNo', etag: etag);
    return (d == null ? null : OrderView.fromJson(d), tag);
  }

  Future<OrderView> cancel(String orderNo) async =>
      OrderView.fromJson(await ref.read(apiClientProvider).call('POST', '/v1/orders/$orderNo/cancel', sign: true, idempotencyKey: const Uuid().v4()));

  Future<(List<OrderView>, String?)> list({String? cursor}) async {
    final d = await ref.read(apiClientProvider).call('GET', '/v1/orders', query: {'limit': ['20'], if (cursor != null) 'cursor': [cursor]});
    return ((d['items'] as List).cast<Map<String, dynamic>>().map(OrderView.fromJson).toList(), d['next_cursor'] as String?);
  }
}

final ordersApiProvider = Provider<OrdersApi>((ref) => OrdersApi(ref));
final ordersListProvider = FutureProvider.autoDispose<List<OrderView>>((ref) async => (await ref.watch(ordersApiProvider).list()).$1);
