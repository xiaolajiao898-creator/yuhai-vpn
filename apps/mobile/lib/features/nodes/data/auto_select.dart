import 'nodes_api.dart';

/// A line can be chosen automatically when this device can actually connect it.
/// WireGuard nodes need a probe endpoint. Egress lines have no public host, so
/// they count only where the Xray engine exists.
bool canAutoSelect(NodeItem node, {required bool egressSupported}) {
  if (!node.available || node.isSaturated) return false;
  if (node.isEgress) return egressSupported;
  return node.probeHost != null &&
      node.probePort != null &&
      node.protocols.isNotEmpty;
}

/// Lowest successful probe. Nodes that did not answer are skipped.
NodeItem? fastestProbed(List<NodeItem> nodes, Map<String, int?> latency) {
  final candidates = [
    for (final node in nodes)
      if (latency[node.id] != null) node
  ]..sort((a, b) => latency[a.id]!.compareTo(latency[b.id]!));
  return candidates.firstOrNull;
}

/// Stable choice when latency cannot be measured: recommended, then name, then id.
NodeItem? preferredEgress(List<NodeItem> nodes) {
  final list = nodes
      .where((node) => node.available && node.isEgress && !node.isSaturated)
      .toList()
    ..sort((a, b) {
      if (a.isRecommended != b.isRecommended) {
        return a.isRecommended ? -1 : 1;
      }
      final byName = a.name.compareTo(b.name);
      return byName != 0 ? byName : a.id.compareTo(b.id);
    });
  return list.firstOrNull;
}
