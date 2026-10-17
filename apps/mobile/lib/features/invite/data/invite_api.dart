import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../app/providers.dart';

class InviteInfo {
  InviteInfo.fromJson(Map<String, dynamic> j)
      : code = j['invite_code'] as String,
        url = j['invite_url'] as String?,
        rules = j['rules'] as String,
        stats = Map<String, dynamic>.from(j['stats'] as Map),
        canBind = j['can_bind'] == true,
        bindingEnabled = j['binding_enabled'] == true,
        rewardsEnabled = j['rewards_enabled'] == true,
        bindStatus = j['bind_status'] as String? ?? 'DISABLED',
        bindDeadline = DateTime.tryParse(j['bind_deadline'] as String? ?? '');
  final String code, rules, bindStatus;
  final String? url;
  final Map<String, dynamic> stats;
  final bool canBind, bindingEnabled, rewardsEnabled;
  final DateTime? bindDeadline;
  bool get acceptsBinding =>
      bindingEnabled && canBind && bindStatus == 'AVAILABLE';
}

class InviteBindResult {
  InviteBindResult.fromJson(Map<String, dynamic> json)
      : bound = json['bound'] == true,
        description = json['reward_description'] as String? ?? '';
  final bool bound;
  final String description;
}

/// 只接受完整邀请码或明确的分享文本，不从任意长字符串中猜测邀请码。
String? parseInviteCode(String input) {
  if (input.length > 4096) return null;
  final text = input.trim().toUpperCase();
  final normalized = text
      .replaceFirst(RegExp(r'^宇海邀请码\s*[:：]?\s*'), '')
      .replaceFirst(RegExp(r'^YHINVITE:\s*'), '')
      .trim();
  return RegExp(r'^[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{6}$').hasMatch(normalized)
      ? normalized
      : null;
}

class InviteReward {
  InviteReward.fromJson(Map<String, dynamic> j)
      : id = j['id'] as String,
        kind = j['kind'] as String,
        status = j['status'] as String,
        days = j['days'] as int?,
        bytes = j['bytes'] as int?,
        amount = j['amount'] as String?,
        isExample = j['is_example'] == true,
        createdAt = DateTime.parse(j['created_at'] as String),
        releaseAt = j['release_at'] == null
            ? null
            : DateTime.parse(j['release_at'] as String);
  final String id, kind, status;
  final int? days, bytes;
  final String? amount;
  final bool isExample;
  final DateTime createdAt;
  final DateTime? releaseAt;
}

class InviteApi {
  InviteApi(this.ref);
  final Ref ref;
  Future<InviteInfo> info() async => InviteInfo.fromJson(
      await ref.read(apiClientProvider).call('GET', '/v1/invite'));
  Future<InviteBindResult> bind(
      {required String code,
      required String method,
      required String idempotencyKey}) async {
    final normalized = parseInviteCode(code);
    if (normalized == null || (method != 'MANUAL' && method != 'CLIPBOARD')) {
      throw ArgumentError('Invalid invite payload');
    }
    return InviteBindResult.fromJson(await ref.read(apiClientProvider).call(
          'POST',
          '/v1/invite/bind',
          json: {'code': normalized, 'method': method},
          sign: true,
          idempotencyKey: idempotencyKey,
        ));
  }

  Future<(List<InviteReward>, String?)> rewards({String? cursor}) async {
    final d = await ref
        .read(apiClientProvider)
        .call('GET', '/v1/invite/rewards', query: {
      'limit': ['20'],
      if (cursor != null) 'cursor': [cursor]
    });
    return (
      (d['items'] as List)
          .map(
              (e) => InviteReward.fromJson(Map<String, dynamic>.from(e as Map)))
          .toList(),
      d['next_cursor'] as String?
    );
  }
}

final inviteApiProvider = Provider<InviteApi>((ref) => InviteApi(ref));
final inviteInfoProvider = FutureProvider.autoDispose<InviteInfo>(
    (ref) => ref.watch(inviteApiProvider).info());
