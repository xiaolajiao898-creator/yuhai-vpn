import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Plain preferences for values that must survive an Android Keystore failure.
/// Device ids and the selected line are not secrets; session tokens stay in
/// secure storage.
abstract interface class DurableIdStore {
  Future<String?> read(String key);
  Future<void> write(String key, String? value);
}

class SharedPrefsDurableIdStore implements DurableIdStore {
  @override
  Future<String?> read(String key) async =>
      (await SharedPreferences.getInstance()).getString(key);

  @override
  Future<void> write(String key, String? value) async {
    final prefs = await SharedPreferences.getInstance();
    if (value == null) {
      await prefs.remove(key);
    } else {
      await prefs.setString(key, value);
    }
  }
}

final durableIdStoreProvider =
    Provider<DurableIdStore>((_) => SharedPrefsDurableIdStore());

class MemoryDurableIdStore implements DurableIdStore {
  final Map<String, String> values = {};

  @override
  Future<String?> read(String key) async => values[key];

  @override
  Future<void> write(String key, String? value) async {
    if (value == null) {
      values.remove(key);
    } else {
      values[key] = value;
    }
  }
}
