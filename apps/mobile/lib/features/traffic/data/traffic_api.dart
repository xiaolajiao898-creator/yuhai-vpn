import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';

class DeviceTraffic {
  const DeviceTraffic({required this.deviceId, required this.bytes});
  final String deviceId;
  final int bytes;

  factory DeviceTraffic.fromJson(Map<String, dynamic> json) => DeviceTraffic(
        deviceId: json['device_id'] as String,
        bytes: _byteCount(json['bytes']),
      );
}

class TrafficDay {
  const TrafficDay({
    required this.date,
    required this.upBytes,
    required this.downBytes,
    this.devices = const [],
  });
  final String date;
  final int upBytes, downBytes;
  final List<DeviceTraffic> devices;
  int get totalBytes => upBytes + downBytes;
  String get shortDate => '${date.substring(5, 7)}/${date.substring(8, 10)}';

  factory TrafficDay.fromJson(Map<String, dynamic> json) {
    final date = json['date'] as String;
    final parsed = DateTime.tryParse('${date}T00:00:00Z');
    if (!RegExp(r'^\d{4}-\d{2}-\d{2}$').hasMatch(date) ||
        parsed == null ||
        parsed.toIso8601String().substring(0, 10) != date) {
      throw const FormatException('Invalid traffic date');
    }
    return TrafficDay(
      date: date,
      upBytes: _byteCount(json['up_bytes']),
      downBytes: _byteCount(json['down_bytes']),
      devices: ((json['by_device'] as List?) ?? const [])
          .map((item) =>
              DeviceTraffic.fromJson(Map<String, dynamic>.from(item as Map)))
          .toList(growable: false),
    );
  }
}

int _byteCount(Object? value) {
  if (value is! int || value < 0) {
    throw const FormatException('Invalid traffic bytes');
  }
  return value;
}

class TrafficApi {
  TrafficApi(this.ref);
  final Ref ref;

  Future<List<TrafficDay>> daily(int days) async {
    if (days != 7 && days != 30) throw ArgumentError.value(days, 'days');
    final data = await ref.read(apiClientProvider).call(
      'GET',
      '/v1/traffic/daily',
      query: {
        'days': ['$days']
      },
    );
    final rows = (data['days'] as List)
        .map((item) =>
            TrafficDay.fromJson(Map<String, dynamic>.from(item as Map)))
        .toList();
    rows.sort((a, b) => a.date.compareTo(b.date));
    if (rows.map((day) => day.date).toSet().length != rows.length ||
        rows.length > days) {
      throw const FormatException('Invalid daily traffic series');
    }
    return rows;
  }
}

final trafficApiProvider = Provider<TrafficApi>((ref) => TrafficApi(ref));

/// 用整数计算展示值，最多两位小数；不插入千分位。二进制单位在页面明确说明。
String formatTrafficBytes(int bytes) {
  if (bytes < 0) throw ArgumentError.value(bytes, 'bytes');
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  final raw = BigInt.from(bytes);
  var divisor = BigInt.one;
  var unit = 0;
  while (unit < units.length - 1 && raw >= divisor * BigInt.from(1024)) {
    divisor *= BigInt.from(1024);
    unit++;
  }
  if (unit == 0) return '$bytes B';
  final hundredths = raw * BigInt.from(100) ~/ divisor;
  final whole = hundredths ~/ BigInt.from(100);
  final fraction = (hundredths % BigInt.from(100))
      .toString()
      .padLeft(2, '0')
      .replaceFirst(RegExp(r'0+$'), '');
  return '$whole${fraction.isEmpty ? '' : '.$fraction'} ${units[unit]}';
}

List<DeviceTraffic> aggregateDeviceTraffic(List<TrafficDay> days) {
  final totals = <String, int>{};
  for (final day in days) {
    for (final device in day.devices) {
      totals.update(device.deviceId, (bytes) => bytes + device.bytes,
          ifAbsent: () => device.bytes);
    }
  }
  return totals.entries
      .map((entry) => DeviceTraffic(deviceId: entry.key, bytes: entry.value))
      .where((device) => device.bytes > 0)
      .toList()
    ..sort((a, b) {
      final order = b.bytes.compareTo(a.bytes);
      return order == 0 ? a.deviceId.compareTo(b.deviceId) : order;
    });
}
