import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/settings/data/connection_preferences.dart';
import 'package:yuhai_vpn/features/settings/presentation/connection_settings_page.dart';

class MemoryPreferences implements ConnectionPreferencesStorage {
  ConnectionPreferences value = const ConnectionPreferences();
  bool failLoad = false, failSave = false;
  Future<void>? holdSave;
  final writes = <ConnectionPreferences>[];

  @override
  Future<ConnectionPreferences> load() async {
    if (failLoad) {
      throw StateError('storage unavailable');
    }
    return value;
  }

  @override
  Future<void> save(ConnectionPreferences preferences) async {
    writes.add(preferences);
    await holdSave;
    if (failSave) {
      throw StateError('storage unavailable');
    }
    value = preferences;
  }
}

Widget preferencesApp(ConnectionPreferencesStorage storage) => ProviderScope(
      overrides: [
        connectionPreferencesStoreProvider.overrideWithValue(storage)
      ],
      child: const MaterialApp(home: ConnectionSettingsPage()),
    );

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('损坏或未知版本的偏好回退默认，协议仅接受约定值', () {
    for (final raw in <String?>[
      null,
      '',
      '[true]',
      '{broken',
      '{"version":2,"protocol":"WIREGUARD","reduce_motion":true}',
      '{"version":1.0,"protocol":"WIREGUARD","reduce_motion":true}',
      '{"version":1,"protocol":"UNKNOWN","reduce_motion":true}',
      '{"version":1,"protocol":"TROJAN","reduce_motion":"yes"}',
      '{"version":1,"protocol":"TROJAN"}',
      'x' * 2049,
    ]) {
      expect(ConnectionPreferences.decode(raw), const ConnectionPreferences());
    }
    expect(ProtocolPreference.shadowsocks2022.wireValue, 'SS2022');
    for (final protocol in ProtocolPreference.values) {
      final preferences =
          ConnectionPreferences(protocol: protocol, reduceMotion: true);
      expect(ConnectionPreferences.decode(jsonEncode(preferences.toJson())),
          preferences);
    }
  });

  test('偏好持久化可重新读取，且不改动账户会话存储', () async {
    FlutterSecureStorage.setMockInitialValues(
        {'session': 'existing-session', 'device_uid': 'existing-device'});
    final storage = SecureConnectionPreferencesStorage();
    const preferences = ConnectionPreferences(
        protocol: ProtocolPreference.wireguard, reduceMotion: true);
    await storage.save(preferences);
    expect(await SecureConnectionPreferencesStorage().load(), preferences);
    final values = await const FlutterSecureStorage().readAll();
    expect(values['session'], 'existing-session');
    expect(values['device_uid'], 'existing-device');
    expect(values.keys.toSet(), {
      'session',
      'device_uid',
      SecureConnectionPreferencesStorage.storageKey
    });
  });

  test('保存失败保留旧状态，重试成功后才更新动画偏好', () async {
    final storage = MemoryPreferences()..failSave = true;
    final container = ProviderContainer(overrides: [
      connectionPreferencesStoreProvider.overrideWithValue(storage)
    ]);
    addTearDown(container.dispose);
    await container.read(connectionPreferencesProvider.future);
    final controller = container.read(connectionPreferencesProvider.notifier);
    await expectLater(controller.setReduceMotion(true), throwsStateError);
    expect(container.read(connectionPreferencesProvider).value,
        const ConnectionPreferences());
    expect(container.read(reduceConnectionMotionProvider), isFalse);
    storage.failSave = false;
    await controller.setReduceMotion(true);
    expect(container.read(reduceConnectionMotionProvider), isTrue);
    expect(storage.value.reduceMotion, isTrue);
  });

  test('并发修改串行合并，保存结束前不发布未落盘状态', () async {
    final release = Completer<void>();
    final storage = MemoryPreferences()..holdSave = release.future;
    final container = ProviderContainer(overrides: [
      connectionPreferencesStoreProvider.overrideWithValue(storage)
    ]);
    addTearDown(container.dispose);
    await container.read(connectionPreferencesProvider.future);
    final controller = container.read(connectionPreferencesProvider.notifier);
    final protocol = controller.setProtocol(ProtocolPreference.wireguard);
    final motion = controller.setReduceMotion(true);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(connectionPreferencesProvider).value,
        const ConnectionPreferences());
    expect(storage.writes.length, 1);
    release.complete();
    await Future.wait([protocol, motion]);
    expect(
        storage.value,
        const ConnectionPreferences(
            protocol: ProtocolPreference.wireguard, reduceMotion: true));
    expect(storage.writes.length, 2);
  });

  testWidgets('设置读取失败可以重试，未支持功能不能开启', (tester) async {
    final storage = MemoryPreferences()..failLoad = true;
    await tester.pumpWidget(preferencesApp(storage));
    await tester.pumpAndSettle();
    expect(find.text('重新读取'), findsOneWidget);
    storage.failLoad = false;
    await tester.tap(find.text('重新读取'));
    await tester.pumpAndSettle();
    final auto = tester.widget<SwitchListTile>(
        find.byKey(const ValueKey('automatic-connect')));
    final killSwitch = tester.widget<ListTile>(
        find.byKey(const ValueKey('kill-switch-guidance')));
    final split = tester.widget<SwitchListTile>(
        find.byKey(const ValueKey('split-tunnel-unavailable')));
    expect(auto.value, isFalse);
    expect(auto.onChanged, isNotNull);
    expect(killSwitch.onTap, isNotNull);
    expect(split.onChanged, isNull);
    expect(find.text('还没做好。\n打开也不会分流'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('协议选择保存并展示，动画保存失败不显示成功', (tester) async {
    final storage = MemoryPreferences();
    await tester.pumpWidget(preferencesApp(storage));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('select-connection-protocol')));
    await tester.pumpAndSettle();
    // The bottom sheet is scrollable on short/landscape screens. The last
    // protocol can be built but outside its viewport until the user scrolls.
    final wireguard = find.byKey(const ValueKey('protocol-WIREGUARD'));
    await tester.ensureVisible(wireguard);
    await tester.pumpAndSettle();
    expect(wireguard.hitTestable(), findsOneWidget);
    await tester.tap(wireguard);
    await tester.pumpAndSettle();
    expect(storage.value.protocol, ProtocolPreference.wireguard);
    expect(find.text('WireGuard'), findsOneWidget);
    expect(find.text('已保存。真正连接时会按这个偏好。'), findsOneWidget);
    storage.failSave = true;
    await tester
        .ensureVisible(find.byKey(const ValueKey('reduce-connection-motion')));
    await tester.tap(find.byKey(const ValueKey('reduce-connection-motion')));
    await tester.pumpAndSettle();
    expect(storage.value.reduceMotion, isFalse);
    final toggle = tester.widget<SwitchListTile>(
        find.byKey(const ValueKey('reduce-connection-motion')));
    expect(toggle.value, isFalse);
    expect(find.text('设置未保存，请重试。仍使用上次保存的设置。'), findsOneWidget);
    expect(find.text('动画设置已保存'), findsNothing);
    expect(tester.takeException(), isNull);
  });
}
