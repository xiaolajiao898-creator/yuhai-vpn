import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/widgets/tide_ring_button.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
import 'package:yuhai_vpn/features/home/presentation/home_page.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

class _FixedStatus extends ConnectionController {
  _FixedStatus(this.status);
  final VpnStatus status;

  @override
  VpnStatus build() => status;
}

Widget homeApp({
  required Map<String, dynamic> me,
  VpnStatus status = VpnStatus.disconnected,
  VpnFailure? failure,
}) {
  return ProviderScope(
    overrides: [
      meProvider.overrideWith((ref) async => me),
      nodesProvider.overrideWith((ref) async => <NodeItem>[]),
      connectionControllerProvider.overrideWith(() => _FixedStatus(status)),
      if (failure != null)
        connectionFailureProvider.overrideWith((ref) => failure),
    ],
    child: const MaterialApp(home: HomePage()),
  );
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  setUp(() {
    FlutterSecureStorage.setMockInitialValues({});
  });

  testWidgets('没有套餐时主操作是免费试用，不把这说成线路故障', (tester) async {
    tester.view.physicalSize = const Size(360, 780);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    await tester.pumpWidget(homeApp(
      me: const {
        'account_no': '10001',
        'entitlement': {'blocked_reason': 'NO_PLAN'},
      },
      status: VpnStatus.error,
      failure: const VpnFailure('NO_PLAN', '没有可用套餐'),
    ));
    await tester.pumpAndSettle();
    expect(find.text('免费试用 24 小时'), findsWidgets);
    expect(find.text('换线路'), findsNothing);
    expect(find.text('连接失败'), findsNothing);
    await tester.tap(find.byType(TideRingButton));
    await tester.pumpAndSettle();
    expect(find.text('还没开通'), findsWidgets);
    expect(find.text('换线路'), findsNothing);
    expect(find.text('重试'), findsNothing);
    expect(tester.takeException(), isNull);
  });

  testWidgets('真实线路失败才提供重试和换线路', (tester) async {
    tester.view.physicalSize = const Size(360, 780);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    await tester.pumpWidget(homeApp(
      me: const {
        'account_no': '10001',
        'entitlement': {
          'plan_name': '月卡',
          'tier': 1,
          'device_count': 1,
          'device_limit': 3,
          'expire_at': '2026-12-01T00:00:00Z',
        },
      },
      status: VpnStatus.error,
      failure: const VpnFailure('HANDSHAKE_TIMEOUT', '线路没有响应'),
    ));
    await tester.pumpAndSettle();
    expect(find.text('连接失败'), findsOneWidget);
    expect(find.text('重试'), findsOneWidget);
    expect(find.text('换线路'), findsOneWidget);
    expect(find.text('免费试用 24 小时'), findsNothing);
    expect(tester.takeException(), isNull);
  });
}
