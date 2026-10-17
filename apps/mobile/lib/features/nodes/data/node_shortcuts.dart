import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// 收藏与最近使用：只存在本机、只存线路编号，不含地址或凭据，也不同步到服务端。
/// 编号失效（线路下架、改为不可用）时照常保留，界面按当前线路列表如实显示为不可用。
class NodeShortcuts {
  const NodeShortcuts({this.favorites = const [], this.recents = const []});

  static const maxFavorites = 50;
  static const maxRecents = 5;

  final List<String> favorites;
  final List<String> recents;

  static bool _validId(Object? v) =>
      v is String && RegExp(r'^[A-Za-z0-9_-]{1,40}$').hasMatch(v);

  NodeShortcuts toggleFavorite(String id) {
    if (!_validId(id)) return this;
    final next = [...favorites];
    if (!next.remove(id)) {
      if (next.length >= maxFavorites) return this;
      next.add(id);
    }
    return NodeShortcuts(favorites: List.unmodifiable(next), recents: recents);
  }

  NodeShortcuts recordRecent(String id) {
    if (!_validId(id)) return this;
    final next = [id, ...recents.where((r) => r != id)].take(maxRecents).toList();
    return NodeShortcuts(favorites: favorites, recents: List.unmodifiable(next));
  }

  Map<String, Object> toJson() =>
      {'version': 1, 'favorites': favorites, 'recents': recents};

  /// 损坏或未知版本的记录一律回到空状态，不报错、不部分采信。
  static NodeShortcuts decode(String? raw) {
    if (raw == null || raw.length > 8192) return const NodeShortcuts();
    try {
      final v = jsonDecode(raw);
      if (v is! Map ||
          v['version'] != 1 ||
          v['favorites'] is! List ||
          v['recents'] is! List) {
        return const NodeShortcuts();
      }
      final fav = (v['favorites'] as List);
      final rec = (v['recents'] as List);
      if (fav.length > maxFavorites ||
          rec.length > maxRecents ||
          !fav.every(_validId) ||
          !rec.every(_validId) ||
          fav.toSet().length != fav.length ||
          rec.toSet().length != rec.length) {
        return const NodeShortcuts();
      }
      return NodeShortcuts(
          favorites: List.unmodifiable(fav.cast<String>()),
          recents: List.unmodifiable(rec.cast<String>()));
    } on FormatException {
      return const NodeShortcuts();
    }
  }

  @override
  bool operator ==(Object other) =>
      other is NodeShortcuts &&
      _eq(other.favorites, favorites) &&
      _eq(other.recents, recents);
  static bool _eq(List<String> a, List<String> b) =>
      a.length == b.length &&
      Iterable<int>.generate(a.length).every((i) => a[i] == b[i]);
  @override
  int get hashCode => Object.hash(Object.hashAll(favorites), Object.hashAll(recents));
}

abstract interface class NodeShortcutsStorage {
  Future<NodeShortcuts> load();
  Future<void> save(NodeShortcuts value);
}

class SecureNodeShortcutsStorage implements NodeShortcutsStorage {
  SecureNodeShortcutsStorage([FlutterSecureStorage? storage])
      : _storage = storage ?? const FlutterSecureStorage();
  final FlutterSecureStorage _storage;
  static const storageKey = 'node_shortcuts_v1';

  @override
  Future<NodeShortcuts> load() async =>
      NodeShortcuts.decode(await _storage.read(key: storageKey));

  @override
  Future<void> save(NodeShortcuts value) =>
      _storage.write(key: storageKey, value: jsonEncode(value.toJson()));
}

/// 只在内存里保存（演示入口与测试用）。
class MemoryNodeShortcutsStorage implements NodeShortcutsStorage {
  NodeShortcuts _value = const NodeShortcuts();
  @override
  Future<NodeShortcuts> load() async => _value;
  @override
  Future<void> save(NodeShortcuts value) async => _value = value;
}

final nodeShortcutsStoreProvider =
    Provider<NodeShortcutsStorage>((_) => SecureNodeShortcutsStorage());

final nodeShortcutsProvider =
    AsyncNotifierProvider<NodeShortcutsController, NodeShortcuts>(
        NodeShortcutsController.new);

class NodeShortcutsController extends AsyncNotifier<NodeShortcuts> {
  Future<void> _writes = Future<void>.value();

  @override
  Future<NodeShortcuts> build() => ref.watch(nodeShortcutsStoreProvider).load();

  Future<void> toggleFavorite(String id) => _update((c) => c.toggleFavorite(id));
  Future<void> recordRecent(String id) => _update((c) => c.recordRecent(id));

  /// 写入串行；保存失败时保留原值，不把没存上的收藏显示成已收藏。
  Future<void> _update(NodeShortcuts Function(NodeShortcuts) change) {
    final storage = ref.read(nodeShortcutsStoreProvider);
    final write = _writes.then((_) async {
      final current = await future;
      final updated = change(current);
      if (updated == current) return;
      await storage.save(updated);
      state = AsyncData(updated);
    });
    _writes = write.then<void>((_) {}, onError: (Object _, StackTrace __) {});
    return write;
  }
}
