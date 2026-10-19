import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import '../../../core/storage/app_secure_storage.dart';

enum ProtocolPreference {
  auto('AUTO', '自动选择'),
  vlessReality('VLESS_REALITY', 'VLESS · Reality'),
  trojan('TROJAN', 'Trojan'),
  shadowsocks2022('SS2022', 'Shadowsocks 2022'),
  wireguard('WIREGUARD', 'WireGuard');

  const ProtocolPreference(this.wireValue, this.label);
  final String wireValue;
  final String label;
}

class ConnectionPreferences {
  const ConnectionPreferences({
    this.protocol = ProtocolPreference.auto,
    this.reduceMotion = false,
  });

  final ProtocolPreference protocol;
  final bool reduceMotion;

  ConnectionPreferences copyWith(
          {ProtocolPreference? protocol, bool? reduceMotion}) =>
      ConnectionPreferences(
          protocol: protocol ?? this.protocol,
          reduceMotion: reduceMotion ?? this.reduceMotion);

  Map<String, Object> toJson() => {
        'version': 1,
        'protocol': protocol.wireValue,
        'reduce_motion': reduceMotion,
      };

  /// Corrupted, unsupported or partially written records use safe defaults.
  /// No setting enables a tunnel or any network protection by itself.
  static ConnectionPreferences decode(String? raw) {
    if (raw == null || raw.length > 2048) {
      return const ConnectionPreferences();
    }
    try {
      final value = jsonDecode(raw);
      if (value is! Map<String, dynamic> ||
          value['version'] is! int ||
          value['version'] != 1 ||
          value['protocol'] is! String ||
          value['reduce_motion'] is! bool) {
        return const ConnectionPreferences();
      }
      for (final protocol in ProtocolPreference.values) {
        if (value['protocol'] == protocol.wireValue) {
          return ConnectionPreferences(
              protocol: protocol, reduceMotion: value['reduce_motion'] as bool);
        }
      }
    } on FormatException {
      return const ConnectionPreferences();
    }
    return const ConnectionPreferences();
  }

  @override
  bool operator ==(Object other) =>
      other is ConnectionPreferences &&
      other.protocol == protocol &&
      other.reduceMotion == reduceMotion;
  @override
  int get hashCode => Object.hash(protocol, reduceMotion);
}

abstract interface class ConnectionPreferencesStorage {
  Future<ConnectionPreferences> load();
  Future<void> save(ConnectionPreferences preferences);
}

/// Device-local preferences only, in a dedicated record; no account/session or
/// account-secret storage is read or written by this component.
class SecureConnectionPreferencesStorage
    implements ConnectionPreferencesStorage {
  SecureConnectionPreferencesStorage([FlutterSecureStorage? storage])
      : _storage = storage ?? appSecureStorage;
  final FlutterSecureStorage _storage;
  static const storageKey = 'connection_preferences_v1';

  @override
  Future<ConnectionPreferences> load() async =>
      ConnectionPreferences.decode(await _storage.read(key: storageKey));

  @override
  Future<void> save(ConnectionPreferences preferences) =>
      _storage.write(key: storageKey, value: jsonEncode(preferences.toJson()));
}

final connectionPreferencesStoreProvider =
    Provider<ConnectionPreferencesStorage>(
        (_) => SecureConnectionPreferencesStorage());

final connectionPreferencesProvider = AsyncNotifierProvider<
    ConnectionPreferencesController,
    ConnectionPreferences>(ConnectionPreferencesController.new);

final reduceConnectionMotionProvider = Provider<bool>((ref) =>
    ref.watch(connectionPreferencesProvider).valueOrNull?.reduceMotion ??
    false);

class ConnectionPreferencesController
    extends AsyncNotifier<ConnectionPreferences> {
  Future<void> _writes = Future<void>.value();
  int _generation = 0;

  @override
  Future<ConnectionPreferences> build() {
    final storage = ref.watch(connectionPreferencesStoreProvider);
    final generation = ++_generation;
    ref.onDispose(() {
      if (_generation == generation) {
        _generation++;
      }
    });
    return storage.load();
  }

  Future<void> setProtocol(ProtocolPreference protocol) =>
      _update((current) => current.copyWith(protocol: protocol));

  Future<void> setReduceMotion(bool enabled) =>
      _update((current) => current.copyWith(reduceMotion: enabled));

  Future<void> _update(
      ConnectionPreferences Function(ConnectionPreferences) change) {
    final generation = _generation;
    final storage = ref.read(connectionPreferencesStoreProvider);
    final write = _writes.then((_) async {
      if (generation != _generation) {
        throw StateError('Preferences page has been disposed');
      }
      final current = await future;
      if (generation != _generation) {
        throw StateError('Preferences page has been disposed');
      }
      final updated = change(current);
      if (updated == current) {
        return;
      }
      await storage.save(updated);
      // Keep the previous value until persistence succeeds; never report an
      // optimistic toggle as saved after storage failure.
      if (generation == _generation) {
        state = AsyncData(updated);
      }
    });
    _writes = write.then<void>((_) {}, onError: (Object _, StackTrace __) {});
    return write;
  }
}
