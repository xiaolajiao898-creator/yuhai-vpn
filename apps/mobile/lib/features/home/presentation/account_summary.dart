/// Home-screen entitlement line. Dates are shown in the device's local time.
String planLabel(Map<String, dynamic> entitlement) {
  final name = entitlement['plan_name'];
  if (name is String && name.trim().isNotEmpty) return name.trim();
  final tier = entitlement['tier'];
  if (tier is int && tier > 0) return 'L$tier 套餐';
  return '套餐';
}

String deviceLabel(Map<String, dynamic> entitlement) {
  final count = entitlement['device_count'];
  final limit = entitlement['device_limit'];
  return '${count is int ? count : 0}/${limit is int ? limit : 0}';
}

String trafficLabel(Map<String, dynamic> entitlement) {
  final quota = entitlement['period_quota_bytes'];
  if (quota is! int || quota < 0) return '不限';
  final used = entitlement['period_used_bytes'];
  final usedBytes = used is int && used >= 0 ? used : 0;
  return '${_bytes(usedBytes)} / ${_bytes(quota)}';
}

/// `raw` is an ISO-8601 timestamp, usually UTC. Missing or unparseable values
/// become an em dash rather than a sliced UTC date.
String formatExpiryLocal(String? raw,
    {DateTime Function(DateTime utc)? toLocal}) {
  if (raw == null || raw.isEmpty) return '—';
  final parsed = DateTime.tryParse(raw);
  if (parsed == null) return '—';
  final local = (toLocal ?? (DateTime value) => value.toLocal())(parsed.toUtc());
  String two(int value) => value.toString().padLeft(2, '0');
  return '${local.year.toString().padLeft(4, '0')}-${two(local.month)}-${two(local.day)} ${two(local.hour)}:${two(local.minute)}';
}

String accountSummaryLine(Map<String, dynamic> entitlement,
    {DateTime Function(DateTime utc)? toLocal}) {
  final blocked = switch (entitlement['blocked_reason']) {
    'NO_PLAN' => '还没有套餐 · 可免费试用 24 小时',
    'EXPIRED' => '套餐已到期 · 去续费',
    'QUOTA_EXHAUSTED' => '流量已用完 · 买流量包',
    'SUSPENDED' => '账户已停用 · 联系客服',
    _ => null,
  };
  if (blocked != null) return blocked;
  final expiry = entitlement['expire_at'];
  return '${planLabel(entitlement)} · 设备 ${deviceLabel(entitlement)} · 流量 ${trafficLabel(entitlement)} · 到期 ${formatExpiryLocal(expiry is String ? expiry : null, toLocal: toLocal)}';
}

String _bytes(int n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var value = n.toDouble();
  var unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  final digits = unit == 0 || value >= 10 ? 0 : 1;
  return '${value.toStringAsFixed(digits)} ${units[unit]}';
}
