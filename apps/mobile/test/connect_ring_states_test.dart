import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/theme/theme.dart';
import 'package:yuhai_vpn/core/widgets/brand.dart';
import 'package:yuhai_vpn/core/widgets/tide_ring_button.dart';
import 'package:yuhai_vpn/features/connection/presentation/connection_controller.dart';
import 'package:yuhai_vpn/features/home/data/me_api.dart';
import 'package:yuhai_vpn/features/home/presentation/home_page.dart';
import 'package:yuhai_vpn/features/update/update_prompt.dart';
import 'package:yuhai_vpn/features/nodes/data/nodes_api.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';

import 'screenshot_fonts.dart';

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
    final shots = Platform.environment['YH_CONNECT_SHOTS'];
    if (shots != null) {
      await tester.runAsync(() async {
        try {
          await loadScreenshotFonts();
        } catch (_) {}
      });
      tester.takeException();
    }
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

  testWidgets('连接环和更新提示可以截图', (tester) async {
    final shots = Platform.environment['YH_CONNECT_SHOTS'];
    if (shots == null) return;
    await _writeDialog(tester, '$shots/update-optional.png', forced: false);
    await _writeDialog(tester, '$shots/update-forced.png', forced: true);
    const cases = <(String, VpnStatus)>[
      ('disconnected', VpnStatus.disconnected),
      ('connecting', VpnStatus.connecting),
      ('connected', VpnStatus.connected),
      ('failed', VpnStatus.error),
    ];
    for (final width in [500.0, 360.0]) {
      for (final (state, status) in cases) {
        await _writeRing(tester, status, width, '$shots/${width == 500 ? 'narrow-500' : 'phone-360'}-$state.png');
      }
    }
  }, timeout: const Timeout(Duration(seconds: 25)));
}

Future<void> _writeRing(WidgetTester tester, VpnStatus status, double width, String path) async {
  final size = (width * 0.92).clamp(228.0, 340.0);
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      backgroundColor: const Color(0xFFF5F0E6),
      body: Center(
        child: RepaintBoundary(
          key: const Key('ring-shot'),
          child: MediaQuery(
            data: const MediaQueryData(disableAnimations: true),
            child: TideRingButton(status: status, onTap: () {}, size: size),
          ),
        ),
      ),
    ),
  ));
  await tester.pump();
  await _png(tester, const Key('ring-shot'), path);
}

Future<void> _writeDialog(WidgetTester tester, String path, {required bool forced}) async {
  await tester.pumpWidget(MaterialApp(
    home: Scaffold(
      backgroundColor: const Color(0xFFF5F0E6),
      body: Center(
        child: RepaintBoundary(
          key: const Key('dialog-shot'),
          child: UpdatePrompt(
            version: '1.2.0',
            notes: '修复连接页，并可以一键升级。',
            forced: forced,
            onUpgrade: () {},
            onLater: forced ? null : () {},
          ),
        ),
      ),
    ),
  ));
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 50));
  debugPrint('dialog pumped');
  await _png(tester, const Key('dialog-shot'), path);
  debugPrint('dialog saved $path');
}

Future<void> _png(WidgetTester tester, Key key, String path) async {
  debugPrint('png start $path');
  final boundary = tester.renderObject<RenderRepaintBoundary>(find.byKey(key));
  final bytes = await tester.runAsync(() async {
    final image = await boundary.toImage(pixelRatio: 2);
    final data = await image.toByteData(format: ui.ImageByteFormat.png);
    image.dispose();
    return data!.buffer.asUint8List();
  });
  final file = File(path);
  await file.parent.create(recursive: true);
  await file.writeAsBytes(bytes!, flush: true);
}
