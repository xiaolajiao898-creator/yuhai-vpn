import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../../../app/providers.dart';

class AccountDevice {
  AccountDevice.fromJson(Map<String, dynamic> json)
      : id = json['id'] as String,
        name = json['display_name'] as String? ?? '',
        platform = json['platform'] as String? ?? '',
        model = json['model'] as String? ?? '',
        isCurrent = json['is_current'] == true,
        lastSeen = DateTime.tryParse(json['last_seen_at'] as String? ?? ''),
        createdAt = DateTime.tryParse(json['created_at'] as String? ?? '');

  final String id, name, platform, model;
  final bool isCurrent;
  final DateTime? lastSeen, createdAt;

  String get displayName => name.isNotEmpty
      ? name
      : model.isNotEmpty
          ? model
          : switch (platform) {
              'IOS' => 'iPhone / iPad',
              'ANDROID' => 'Android 设备',
              _ => '我的设备',
            };
}

class DeviceList {
  const DeviceList(this.devices, this.limit);
  final List<AccountDevice> devices;
  final int limit;
}

class AccountApi {
  AccountApi(this.ref);
  final Ref ref;

  Future<DeviceList> devices() async {
    final data = await ref.read(apiClientProvider).call('GET', '/v1/devices');
    return DeviceList(
      ((data['devices'] as List?) ?? const [])
          .map((item) =>
              AccountDevice.fromJson(Map<String, dynamic>.from(item as Map)))
          .toList(),
      data['device_limit'] as int? ?? 0,
    );
  }

  Future<void> removeDevice(String id) async {
    await ref.read(apiClientProvider).call(
          'DELETE',
          '/v1/devices/${Uri.encodeComponent(id)}',
          sign: true,
          idempotencyKey:
              const Uuid().v5(Namespace.url.value, 'yuhai:device:remove:$id'),
        );
  }

  Future<void> logout() async {
    await ref
        .read(apiClientProvider)
        .call('POST', '/v1/auth/logout', sign: true);
  }
}

final accountApiProvider = Provider<AccountApi>((ref) => AccountApi(ref));
final devicesProvider = FutureProvider.autoDispose<DeviceList>(
    (ref) => ref.watch(accountApiProvider).devices());

/// 金额来自服务端字符串，显示时只去掉小数末尾的零，不转浮点数。
String accountBalanceLabel(Object? value) {
  final text = value is String ? value : '0';
  if (!RegExp(r'^\d+(\.\d+)?$').hasMatch(text)) return '—';
  return text.contains('.')
      ? text.replaceFirst(RegExp(r'0+$'), '').replaceFirst(RegExp(r'\.$'), '')
      : text;
}

String accountDateLabel(DateTime? value) {
  if (value == null) return '暂无记录';
  final date = value.toLocal();
  String two(int v) => v.toString().padLeft(2, '0');
  return '${date.year}-${two(date.month)}-${two(date.day)} ${two(date.hour)}:${two(date.minute)}';
}
