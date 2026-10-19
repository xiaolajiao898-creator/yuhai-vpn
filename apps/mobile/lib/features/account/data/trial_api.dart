import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../../../app/providers.dart';
import '../../../core/storage/durable_id_store.dart';

/// 装在本机、注册第二个账户时也不轮换的安装标识。
/// 服务端用它限制「一台手机反复开新账户领试用」。改客户端可以换一个新值，这只拦住正式 App。
const trialInstallStorageKey = 'trial_install_id_v1';

class TrialStatus {
  const TrialStatus({
    required this.enabled,
    required this.eligible,
    required this.status,
    this.reason,
    this.expireAt,
    required this.tier,
    required this.deviceLimit,
    required this.unlimited,
    required this.durationHours,
  });

  final bool enabled, eligible, unlimited;
  final String status;
  final String? reason;
  final DateTime? expireAt;
  final int tier, deviceLimit, durationHours;

  static TrialStatus fromJson(Map<String, dynamic> j) => TrialStatus(
        enabled: j['enabled'] == true,
        eligible: j['eligible'] == true,
        status: j['status'] is String ? j['status'] as String : 'available',
        reason: j['reason'] as String?,
        expireAt: j['expire_at'] is String ? DateTime.tryParse(j['expire_at'] as String) : null,
        tier: j['tier'] is int ? j['tier'] as int : 1,
        deviceLimit: j['device_limit'] is int ? j['device_limit'] as int : 3,
        unlimited: j['unlimited_traffic'] != false,
        durationHours: j['duration_hours'] is int ? j['duration_hours'] as int : 24,
      );
}

class TrialGrant {
  const TrialGrant({
    required this.status,
    required this.expireAt,
    required this.tier,
    required this.deviceLimit,
    required this.unlimited,
    required this.durationHours,
    required this.active,
  });

  final String status;
  final DateTime expireAt;
  final int tier, deviceLimit, durationHours;
  final bool unlimited, active;

  static TrialGrant fromJson(Map<String, dynamic> j) => TrialGrant(
        status: j['status'] as String,
        expireAt: DateTime.parse(j['expire_at'] as String),
        tier: j['tier'] as int,
        deviceLimit: j['device_limit'] as int,
        unlimited: j['unlimited_traffic'] == true,
        durationHours: j['duration_hours'] as int,
        active: j['active'] == true,
      );
}

final trialApiProvider = Provider<TrialApi>((ref) => TrialApi(ref));

class TrialApi {
  TrialApi(this.ref);
  final Ref ref;

  Future<String> installId() async {
    final store = ref.read(durableIdStoreProvider);
    final existing = await store.read(trialInstallStorageKey);
    if (existing != null && _uuid.hasMatch(existing)) return existing;
    final created = const Uuid().v4();
    await store.write(trialInstallStorageKey, created);
    return created;
  }

  Future<TrialStatus> status() async => TrialStatus.fromJson(
      await ref.read(apiClientProvider).call('GET', '/v1/trial'));

  Future<TrialGrant> claim() async {
    final id = await installId();
    return TrialGrant.fromJson(await ref.read(apiClientProvider).call(
        'POST', '/v1/trial',
        json: {'install_id': id}, sign: true));
  }
}

final _uuid = RegExp(
    r'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$');
