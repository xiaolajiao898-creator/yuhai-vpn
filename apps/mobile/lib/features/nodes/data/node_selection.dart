import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/storage/durable_id_store.dart';

/// Selection is a preference, never a permission to reuse stale connection data.
final selectedNodeProvider = StateProvider<String?>((_) => null);
final latencyProvider = StateProvider<Map<String, int?>>((_) => {});

const selectedNodeStorageKey = 'selected_node_id_v1';
final _nodeId = RegExp(r'^[A-Za-z0-9_-]{1,40}$');

/// Bumped when the choice is explicitly forgotten, so a load still in flight
/// cannot restore a line after logout.
final selectedNodeEpochProvider = StateProvider<int>((_) => 0);

class NodeSelectionStore {
  NodeSelectionStore(this._ids);
  final DurableIdStore _ids;

  Future<String?> load() async {
    final raw = await _ids.read(selectedNodeStorageKey);
    if (raw == null || !_nodeId.hasMatch(raw)) return null;
    return raw;
  }

  Future<void> save(String? id) {
    if (id != null && !_nodeId.hasMatch(id)) return Future<void>.value();
    return _ids.write(selectedNodeStorageKey, id);
  }
}

final nodeSelectionStoreProvider = Provider<NodeSelectionStore>(
    (ref) => NodeSelectionStore(ref.watch(durableIdStoreProvider)));

/// Restores the last line once, then stores later changes. Storage errors stay
/// local: they never clear the account session.
final selectedNodeHydrationProvider = Provider<void>((ref) {
  final store = ref.watch(nodeSelectionStoreProvider);
  final epoch = ref.read(selectedNodeEpochProvider);
  var settled = false;
  var touched = false;
  ref.listen<String?>(selectedNodeProvider, (_, next) {
    if (!settled) {
      touched = true;
      return;
    }
    unawaited(store.save(next).catchError((Object _) {}));
  });
  unawaited(() async {
    try {
      final saved = await store.load();
      if (ref.read(selectedNodeEpochProvider) != epoch) return;
      settled = true;
      if (!touched && saved != null && ref.read(selectedNodeProvider) == null) {
        ref.read(selectedNodeProvider.notifier).state = saved;
      } else if (touched) {
        await store.save(ref.read(selectedNodeProvider));
      }
    } catch (_) {
      settled = true;
    }
  }());
});

void forgetSelectedNode(Ref ref) {
  ref.read(selectedNodeEpochProvider.notifier).update((value) => value + 1);
  ref.invalidate(selectedNodeProvider);
  unawaited(
      ref.read(nodeSelectionStoreProvider).save(null).catchError((Object _) {}));
}
