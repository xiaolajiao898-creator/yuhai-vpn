import 'nodes_api.dart';

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
