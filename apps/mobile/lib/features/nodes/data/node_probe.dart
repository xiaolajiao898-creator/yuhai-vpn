import 'nodes_api.dart';

/// 一条线路的多次 TCP 连接。中位数是成功那几次的耗时，失败次数不是丢包率，也不是下载速度。
class ProbeQuality {
  const ProbeQuality({this.medianMs, required this.attempts, required this.failures});
  final int? medianMs;
  final int attempts;
  final int failures;
}

/// 只描述探测端口的往返，不表示带宽。
String signalLabel(int? medianMs) {
  final ms = medianMs;
  if (ms == null || ms < 0) return '';
  if (ms < 100) return '信号好';
  if (ms < 200) return '信号中';
  if (ms < 400) return '信号弱';
  return '信号差';
}

int? medianLatency(List<int> samples) {
  if (samples.isEmpty) return null;
  final sorted = [...samples]..sort();
  final mid = sorted.length ~/ 2;
  if (sorted.length.isOdd) return sorted[mid];
  return ((sorted[mid - 1] + sorted[mid]) / 2).round();
}

/// 用户时区附近的国家。马来西亚（UTC+8）先测新加坡、香港、日本，而不是列表开头的德国。
List<String> nearbyCountryCodes(Duration offset) {
  final hours = offset.inMinutes / 60.0;
  if (hours >= 6.5 && hours <= 10) {
    return const ['SG', 'HK', 'JP', 'TW', 'KR', 'MY', 'TH', 'VN', 'PH', 'AU'];
  }
  if (hours >= 4 && hours < 6.5) {
    return const ['SG', 'HK', 'IN', 'JP', 'AE'];
  }
  if (hours >= 1 && hours < 4) {
    return const ['DE', 'NL', 'FR', 'GB', 'SG'];
  }
  if (hours >= -1 && hours < 1) {
    return const ['GB', 'DE', 'NL', 'FR', 'SG'];
  }
  if (hours <= -3 && hours >= -10) {
    return const ['US', 'CA', 'JP', 'SG', 'HK'];
  }
  return const ['SG', 'JP', 'HK', 'DE', 'US'];
}

/// 每个国家抽 2–3 条，附近的国家先抽，总数有上限。不再只拿列表前 20 条。
List<NodeItem> selectProbeTargets(
  List<NodeItem> nodes, {
  List<String>? preferCountries,
  int perCountry = 3,
  int maxTargets = 24,
}) {
  final prefer = preferCountries ??
      nearbyCountryCodes(DateTime.now().timeZoneOffset);
  final groups = <String, List<NodeItem>>{};
  for (final node in nodes) {
    if (!node.available || node.probeHost == null || node.probePort == null) {
      continue;
    }
    final code = node.countryCode.trim().toUpperCase();
    groups.putIfAbsent(code.isEmpty ? 'ZZ' : code, () => []).add(node);
  }
  final countries = groups.keys.toList()
    ..sort((a, b) {
      final ar = prefer.indexOf(a);
      final br = prefer.indexOf(b);
      final av = ar < 0 ? 1000 : ar;
      final bv = br < 0 ? 1000 : br;
      if (av != bv) return av.compareTo(bv);
      return a.compareTo(b);
    });
  final taken = {for (final code in countries) code: 0};
  final out = <NodeItem>[];
  var progressed = true;
  while (out.length < maxTargets && progressed) {
    progressed = false;
    for (final code in countries) {
      if (out.length >= maxTargets) break;
      final list = groups[code]!;
      final index = taken[code]!;
      if (index >= perCountry || index >= list.length) continue;
      out.add(list[index]);
      taken[code] = index + 1;
      progressed = true;
    }
  }
  return out;
}

/// A visible probe round has a bounded target set, concurrency and deadline.
/// Cancellation stops scheduling; results of already-running probes are discarded.
Future<Map<String, int?>> probeNodes(
  List<NodeItem> nodes,
  Future<int?> Function(String host, int port) probe, {
  required bool Function() isCurrent,
  Duration timeout = const Duration(seconds: 2),
  List<String>? preferCountries,
}) async {
  final targets = selectProbeTargets(nodes, preferCountries: preferCountries);
  var cursor = 0;
  final results = <String, int?>{};
  Future<void> worker() async {
    while (isCurrent() && cursor < targets.length) {
      final n = targets[cursor++];
      int? ms;
      try {
        ms = await probe(n.probeHost!, n.probePort!).timeout(timeout);
      } catch (_) {
        ms = null;
      }
      if (!isCurrent()) return;
      results[n.id] =
          ms != null && ms >= 0 && ms <= timeout.inMilliseconds ? ms : null;
    }
  }

  await Future.wait(List.generate(4, (_) => worker()));
  return Map<String, int?>.unmodifiable(results);
}

/// 每条线路连 4 次。取消后不再继续采样。
Future<Map<String, ProbeQuality>> probeNodeQuality(
  List<NodeItem> nodes,
  Future<int?> Function(String host, int port) probe, {
  required bool Function() isCurrent,
  Duration timeout = const Duration(seconds: 2),
  int samples = 4,
  List<String>? preferCountries,
}) async {
  final targets =
      selectProbeTargets(nodes, preferCountries: preferCountries);
  var cursor = 0;
  final results = <String, ProbeQuality>{};
  Future<void> worker() async {
    while (isCurrent() && cursor < targets.length) {
      final n = targets[cursor++];
      final ok = <int>[];
      var failures = 0;
      for (var i = 0; i < samples; i++) {
        if (!isCurrent()) return;
        int? ms;
        try {
          ms = await probe(n.probeHost!, n.probePort!).timeout(timeout);
        } catch (_) {
          ms = null;
        }
        if (!isCurrent()) return;
        if (ms != null && ms >= 0 && ms <= timeout.inMilliseconds) {
          ok.add(ms);
        } else {
          failures++;
        }
      }
      if (!isCurrent()) return;
      results[n.id] = ProbeQuality(
          medianMs: medianLatency(ok), attempts: samples, failures: failures);
    }
  }

  await Future.wait(List.generate(4, (_) => worker()));
  return Map<String, ProbeQuality>.unmodifiable(results);
}
