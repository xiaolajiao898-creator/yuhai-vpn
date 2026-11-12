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

/// A visible probe round has a bounded target set, concurrency and deadline.
/// Cancellation stops scheduling; results of already-running probes are discarded.
Future<Map<String, int?>> probeNodes(
  List<NodeItem> nodes,
  Future<int?> Function(String host, int port) probe, {
  required bool Function() isCurrent,
  Duration timeout = const Duration(seconds: 2),
}) async {
  final targets = nodes
      .where((n) => n.available && n.probeHost != null && n.probePort != null)
      .take(20)
      .toList();
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
}) async {
  final targets = nodes
      .where((n) => n.available && n.probeHost != null && n.probePort != null)
      .take(20)
      .toList();
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
