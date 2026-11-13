import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/core/widgets/brand.dart';
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

Widget _home({required VpnStatus status}) {
  return ProviderScope(
    overrides: [
      meProvider.overrideWith((ref) async => const {
            'account_no': 'YH888876',
            'entitlement': {
              'plan_name': '季卡',
              'tier': 1,
              'device_count': 1,
              'device_limit': 3,
              'expire_at': '2026-12-01T00:00:00Z',
            },
          }),
      nodesProvider.overrideWith((ref) async => <NodeItem>[]),
      connectionControllerProvider.overrideWith(() => _FixedStatus(status)),
    ],
    child: MaterialApp(
      theme: buildDeepSeaTheme(),
      home: const RepaintBoundary(key: Key('shot'), child: HomePage()),
    ),
  );
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  setUp(() {
    FlutterSecureStorage.setMockInitialValues({});
  });

  testWidgets('播放键只打招呼，点圆环或胶囊才连接', (tester) async {
    var taps = 0;
    await tester.pumpWidget(MaterialApp(
      home: Center(
        child: TideRingButton(status: VpnStatus.disconnected, onTap: () => taps++, size: 300),
      ),
    ));
    await tester.pump();
    expect(find.text('轻触连接'), findsOneWidget);
    expect(find.byType(AiliFigure), findsOneWidget);
    await tester.tap(find.bySemanticsLabel('打招呼'));
    await tester.pump();
    expect(taps, 0);
    await tester.tap(find.text('轻触连接'));
    await tester.pump();
    expect(taps, 1);
    await tester.tap(find.byType(TideRingButton));
    await tester.pump();
    expect(taps, 2);
    expect(tester.takeException(), isNull);
  });

  testWidgets('四种连接状态在窄窗口和手机宽度都不溢出', (tester) async {
    const cases = <(String, VpnStatus, String)>[
      ('disconnected', VpnStatus.disconnected, '轻触连接'),
      ('connecting', VpnStatus.connecting, '轻触连接'),
      ('connected', VpnStatus.connected, '轻触断开'),
      ('failed', VpnStatus.error, '轻触连接'),
    ];
    const windows = <(String, Size)>[
      ('narrow-500', Size(500, 900)),
      ('phone-360', Size(360, 800)),
    ];
    for (final (_, size) in windows) {
      for (final (state, status, label) in cases) {
        tester.view.devicePixelRatio = 1;
        tester.view.physicalSize = size;
        await tester.pumpWidget(_home(status: status));
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 50));
        expect(find.text(label == '轻触断开' ? '已连接' : '未连接').evaluate().isNotEmpty || find.text(label).evaluate().isNotEmpty, isTrue);
        expect(find.byType(AiliFigure), findsOneWidget);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(MaterialApp(
          home: Scaffold(
            backgroundColor: const Color(0xFFF5F0E6),
            body: Center(child: TideRingButton(status: status, onTap: () {}, size: (size.width * 0.92).clamp(228.0, 340.0))),
          ),
        ));
        await tester.pump();
        expect(find.text(label), findsOneWidget);
      }
    }
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
  });
}
