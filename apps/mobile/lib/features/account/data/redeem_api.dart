import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';

class RedeemResult {
  RedeemResult.fromJson(Map<String, dynamic> json)
      : cardName = json['card_name'] as String? ?? '兑换卡',
        days = json['duration_days'] as int? ?? 0,
        tier = json['tier'] as int? ?? 0,
        deviceLimit = json['device_limit'] as int? ?? 0,
        unlimited = json['unlimited_traffic'] == true,
        expireAt = DateTime.tryParse(json['expire_at'] as String? ?? ''),
        extended = json['extended'] == true;

  final String cardName;
  final int days, tier, deviceLimit;
  final bool unlimited, extended;
  final DateTime? expireAt;
}

class RedeemApi {
  RedeemApi(this.ref);
  final Ref ref;

  Future<RedeemResult> redeem(String code) async {
    final data = await ref.read(apiClientProvider).call(
          'POST',
          '/v1/redeem-codes',
          json: {'code': code.trim()},
          sign: true,
        );
    return RedeemResult.fromJson(data);
  }
}

final redeemApiProvider = Provider<RedeemApi>((ref) => RedeemApi(ref));
