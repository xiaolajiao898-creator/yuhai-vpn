import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../app/providers.dart';

class LedgerEntry {
  const LedgerEntry({required this.id, required this.kind, required this.createdAt, this.amount, this.seconds, this.bytes, this.devices, this.note});
  final String id, kind;
  final DateTime createdAt;
  final String? amount, note;
  final int? seconds, bytes, devices;
  factory LedgerEntry.fromJson(Map<String, dynamic> j) => LedgerEntry(
    id: j['id'] as String, kind: j['kind'] as String, createdAt: DateTime.parse(j['created_at'] as String),
    amount: j['amount'] as String?, seconds: j['seconds_delta'] as int?, bytes: j['bytes_delta'] as int?, devices: j['devices_delta'] as int?, note: j['note'] as String?,
  );
}

class LedgerBatch {
  const LedgerBatch(this.items, this.nextCursor);
  final List<LedgerEntry> items;
  final String? nextCursor;
}

class LedgerApi {
  LedgerApi(this.ref);
  final Ref ref;
  Future<LedgerBatch> list(bool entitlements, {String? cursor}) async {
    final data = await ref.read(apiClientProvider).call('GET', entitlements ? '/v1/entitlements/ledger' : '/v1/balance/ledger',
      query: {'limit': ['20'], if (cursor != null) 'cursor': [cursor]});
    return LedgerBatch((data['items'] as List).map((e) => LedgerEntry.fromJson(Map<String, dynamic>.from(e as Map))).toList(), data['next_cursor'] as String?);
  }
}
final ledgerApiProvider = Provider<LedgerApi>((ref) => LedgerApi(ref));
