import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/storage/durable_id_store.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';

class GatedStore implements DurableIdStore {
  GatedStore(this.inner);
  final MemoryDurableIdStore inner;
  Future<void> Function()? beforeRead;

  @override
  Future<String?> read(String key) async {
    final seen = inner.values[key];
    final wait = beforeRead;
    if (wait != null) await wait();
    return seen;
  }

  @override
  Future<void> write(String key, String? value) => inner.write(key, value);
}

class _Forget extends Notifier<void> {
  @override
  void build() {}

  void go() => forgetSelectedNode(ref);
}

final _forget = NotifierProvider<_Forget, void>(_Forget.new);

void forget(ProviderContainer container) {
  container.read(_forget.notifier).go();
}

void main() {
  test('重启后恢复上次选择', () async {
    final ids = MemoryDurableIdStore();
    await ids.write(selectedNodeStorageKey, 'e12');
    final container = ProviderContainer(overrides: [
      durableIdStoreProvider.overrideWithValue(ids),
    ]);
    addTearDown(container.dispose);
    container.read(selectedNodeHydrationProvider);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(selectedNodeProvider), 'e12');
  });

  test('退出后进行中的读取不能把线路写回来', () async {
    final ids = MemoryDurableIdStore();
    await ids.write(selectedNodeStorageKey, 'e12');
    final store = GatedStore(ids);
    final gate = Future<void>.delayed(const Duration(milliseconds: 30));
    store.beforeRead = () => gate;
    final container = ProviderContainer(overrides: [
      durableIdStoreProvider.overrideWithValue(store),
    ]);
    addTearDown(container.dispose);
    container.read(selectedNodeHydrationProvider);
    forget(container);
    await gate;
    await Future<void>.delayed(Duration.zero);
    expect(container.read(selectedNodeProvider), isNull);
    expect(ids.values[selectedNodeStorageKey], isNull);
  });
}
