import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';
import 'package:yuhai_vpn/features/account/data/account_api.dart';
import 'package:yuhai_vpn/features/account/presentation/account_page.dart';
import 'package:yuhai_vpn/features/account/presentation/devices_page.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/onboarding_controller.dart';
import 'package:yuhai_vpn/features/settings/data/device_removal_auth.dart';
import 'package:yuhai_vpn/platform/vpn/mock_vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/features/nodes/data/node_selection.dart';

class FakeAccountApi implements AccountApi {
  FakeAccountApi(this.events);
  final List<String> events;
  bool failDevices = false, failLogout = false;
  var listCalls = 0;
  var removed = false;
  @override
  Ref get ref => throw UnimplementedError();
  @override
  Future<DeviceList> devices() async {
    listCalls++;
    if (failDevices) throw Exception('offline');
    AccountDevice device(String id, bool current) => AccountDevice.fromJson({
          'id': id,
          'display_name': current ? '我的手机' : '备用手机',
          'platform': 'ANDROID',
          'is_current': current,
          'created_at': '2026-09-12T00:00:00Z',
          'last_seen_at': '2026-09-12T02:00:00Z',
        });
    return DeviceList([device('1', true), if (!removed) device('2', false)], 3);
  }

  @override
  Future<void> removeDevice(String id) async {
    events.add('remove:$id');
    removed = true;
  }

  @override
  Future<void> logout() async {
    events.add('server-logout');
    if (failLogout) throw Exception('offline');
  }
}

class FakeSessionController extends SessionController {
  FakeSessionController(this.events);
  final List<String> events;
  @override
  Future<Session?> build() async => null;
  @override
  Future<void> signOut() async {
    events.add('local-signout');
  }
}

class FakeEngine extends MockVpnEngine {
  FakeEngine(this.events);
  final List<String> events;
  @override
  Future<void> disconnect() async {
    events.add('disconnect');
  }
}

class PendingLogoutEngine extends FakeEngine {
  PendingLogoutEngine(super.events);
  final permission = Completer<bool>();
  final stopped = Completer<void>();
  var preparations = 0, connections = 0;
  @override
  Future<bool> prepare() {
    preparations++;
    return permission.future;
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    connections++;
  }

  @override
  Future<void> disconnect() async {
    events.add('disconnect');
    await stopped.future;
  }
}

class FakeRemovalAuth extends DeviceRemovalAuthenticator {
  FakeRemovalAuth({this.allowed = true, this.fail = false});
  final bool allowed, fail;
  @override
  Future<bool> confirmIdentity() async {
    if (fail) throw Exception('system unavailable');
    return allowed;
  }
}

Widget app(FakeAccountApi api, Widget page,
        {FakeRemovalAuth? auth, FakeEngine? engine}) =>
    ProviderScope(overrides: [
      accountApiProvider.overrideWithValue(api),
      deviceRemovalAuthProvider.overrideWithValue(auth ?? FakeRemovalAuth()),
      meProvider.overrideWith((_) async => {
            'account_no': '8302571644',
            'status': 'ACTIVE',
            'backup_acknowledged': false,
            'balance': '1200.500000',
            'entitlement': {'device_count': 2, 'device_limit': 3},
          }),
      sessionProvider.overrideWith(() => FakeSessionController(api.events)),
      vpnEngineProvider.overrideWithValue(engine ?? FakeEngine(api.events)),
      if (engine != null) vpnDemoModeProvider.overrideWithValue(true),
      if (engine != null)
        nodesProvider.overrideWith((_) async => [
              const NodeItem(
                id: '1',
                name: '测试线路',
                countryCode: 'HK',
                city: '香港',
                region: 'ASIA',
                available: true,
                load: 10,
                isRecommended: false,
                probeHost: 'test.invalid',
                probePort: 443,
                protocols: ['MOCK'],
              )
            ]),
    ], child: MaterialApp(home: page));

void main() {
  testWidgets('点击退出后等待断开期间，迟到的连接权限不会重新连接', (tester) async {
    final api = FakeAccountApi([]);
    final engine = PendingLogoutEngine(api.events);
    addTearDown(engine.dispose);
    await tester.pumpWidget(app(api, const AccountPage(), engine: engine));
    await tester.pumpAndSettle();
    final container =
        ProviderScope.containerOf(tester.element(find.byType(AccountPage)));
    container.read(selectedNodeProvider.notifier).state = '1';
    final connecting =
        container.read(connectionControllerProvider.notifier).toggle();
    await tester.pumpAndSettle();
    expect(engine.preparations, 1);
    await tester.ensureVisible(find.text('退出当前账户'));
    await tester.tap(find.text('退出当前账户'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('已保存密钥，退出'));
    await tester.pump();
    expect(api.events, ['disconnect']);
    engine.permission.complete(true);
    await tester.pump();
    await connecting;
    expect(engine.connections, 0);
    engine.stopped.complete();
    await tester.pumpAndSettle();
    expect(api.events, ['disconnect', 'server-logout', 'local-signout']);
    expect(tester.takeException(), isNull);
  });
  testWidgets('设备请求失败可重试且当前设备没有移除按钮', (tester) async {
    final api = FakeAccountApi([])..failDevices = true;
    await tester.pumpWidget(app(api, const DevicesPage()));
    await tester.pumpAndSettle();
    expect(find.text('重新加载'), findsOneWidget);
    api.failDevices = false;
    await tester.tap(find.text('重新加载'));
    await tester.pumpAndSettle();
    expect(api.listCalls, 2);
    expect(find.text('当前设备'), findsOneWidget);
    expect(find.byKey(const ValueKey('remove-device-1')), findsNothing);
    expect(find.byKey(const ValueKey('remove-device-2')), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('移除设备需要确认，成功后刷新设备数量', (tester) async {
    final api = FakeAccountApi([]);
    await tester.pumpWidget(app(api, const DevicesPage()));
    await tester.pumpAndSettle();
    await tester.tap(find.text('移除设备'));
    await tester.pumpAndSettle();
    expect(api.events, isEmpty);
    await tester.tap(find.text('取消'));
    await tester.pumpAndSettle();
    expect(api.events, isEmpty);
    await tester.tap(find.text('移除设备'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('确认移除'));
    await tester.pumpAndSettle();
    expect(api.events, ['remove:2']);
    expect(find.text('备用手机'), findsNothing);
    expect(find.text('1 / 3'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets('未备份退出强提示，服务器失败时保留本地会话', (tester) async {
    final api = FakeAccountApi([])..failLogout = true;
    await tester.pumpWidget(app(api, const AccountPage()));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('退出当前账户'));
    await tester.tap(find.text('退出当前账户'));
    await tester.pumpAndSettle();
    expect(find.textContaining('你还没有确认备份'), findsOneWidget);
    await tester.tap(find.text('已保存密钥，退出'));
    await tester.pumpAndSettle();
    expect(api.events, ['disconnect', 'server-logout']);
    expect(find.textContaining('退出未完成'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  for (final fail in [false, true]) {
    testWidgets(fail ? '系统身份验证不可用时禁止移除请求' : '取消身份验证时禁止移除请求', (tester) async {
      final api = FakeAccountApi([]);
      await tester.pumpWidget(app(api, const DevicesPage(),
          auth: FakeRemovalAuth(allowed: false, fail: fail)));
      await tester.pumpAndSettle();
      await tester.tap(find.text('移除设备'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('确认移除'));
      await tester.pumpAndSettle();
      expect(api.events, isEmpty);
      expect(find.text('备用手机'), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  }

  testWidgets('退出必须服务器撤销成功后才清本地登录', (tester) async {
    final api = FakeAccountApi([]);
    await tester.pumpWidget(app(api, const AccountPage()));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.text('退出当前账户'));
    await tester.tap(find.text('退出当前账户'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('已保存密钥，退出'));
    await tester.pumpAndSettle();
    expect(api.events, ['disconnect', 'server-logout', 'local-signout']);
    expect(tester.takeException(), isNull);
  });

  test('金额显示不转换浮点、不加千分位、不误删整数末尾零', () {
    expect(accountBalanceLabel('1200.500000'), '1200.5');
    expect(accountBalanceLabel('1200'), '1200');
    expect(accountBalanceLabel('0.000001'), '0.000001');
    expect(accountBalanceLabel('0.000000'), '0');
  });
}
