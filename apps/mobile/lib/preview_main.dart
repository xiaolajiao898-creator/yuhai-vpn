import 'dart:math' as math;
import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'app/providers.dart';
import 'app/router.dart';
import 'app/theme/theme.dart';
import 'app/theme/tokens.dart';
import 'preview/preview_api.dart';
import 'features/settings/data/device_removal_auth.dart';
import 'features/nodes/data/node_shortcuts.dart';
import 'features/nodes/data/nodes_api.dart';
import 'features/connection/presentation/connection_controller.dart';
import 'platform/vpn/mock_vpn_engine.dart';
import 'features/settings/data/connection_preferences.dart';

/// Separate entry point: production main.dart never imports demo fixtures.
void main() => runApp(const PreviewHost());

class PreviewHost extends StatefulWidget {
  const PreviewHost({super.key});
  @override
  State<PreviewHost> createState() => _PreviewHostState();
}

class _PreviewHostState extends State<PreviewHost> {
  int _generation = 0;
  late PreviewSessionStore _store;
  late PreviewApi _api;
  late PreviewPreferencesStorage _preferences;
  late MemoryNodeShortcutsStorage _shortcuts;
  late SemanticsHandle _semantics;
  @override
  void initState() {
    super.initState();
    _semantics = SemanticsBinding.instance.ensureSemantics();
    _resetData();
  }

  @override
  void dispose() {
    _semantics.dispose();
    super.dispose();
  }

  void _resetData() {
    _store = PreviewSessionStore();
    _api = PreviewApi(_store);
    _preferences = PreviewPreferencesStorage();
    _shortcuts = MemoryNodeShortcutsStorage();
  }

  @override
  Widget build(BuildContext context) => ProviderScope(
        key: ValueKey(_generation),
        overrides: [
          nodeDemoModeProvider.overrideWithValue(true),
          vpnDemoModeProvider.overrideWithValue(true),
          vpnEngineProvider.overrideWith((ref) {
            final engine = MockVpnEngine();
            ref.onDispose(engine.dispose);
            return engine;
          }),
          sessionStoreProvider.overrideWithValue(_store),
          apiClientProvider.overrideWithValue(_api),
          deviceKeyProvider.overrideWithValue(PreviewDeviceKey()),
          connectionPreferencesStoreProvider.overrideWithValue(_preferences),
          nodeShortcutsStoreProvider.overrideWithValue(_shortcuts),
          deviceRemovalAuthProvider.overrideWithValue(_PreviewDeviceAuth())
        ],
        child: _PreviewApp(
            onReset: () => setState(() {
                  _generation++;
                  _resetData();
                })),
      );
}

// Only the in-memory demo may simulate this check. Native builds use local_auth.
class _PreviewDeviceAuth extends DeviceRemovalAuthenticator {
  @override
  Future<bool> confirmIdentity() async => true;
}

class _PreviewApp extends ConsumerWidget {
  const _PreviewApp({required this.onReset});
  final VoidCallback onReset;
  @override
  Widget build(BuildContext context, WidgetRef ref) => MaterialApp.router(
        title: '宇海 · 前端演示',
        theme: buildDeepSeaTheme(),
        debugShowCheckedModeBanner: false,
        routerConfig: ref.watch(routerProvider),
        builder: (context, child) => DecoratedBox(
          decoration: const BoxDecoration(
              gradient: RadialGradient(
                  center: Alignment.topLeft,
                  radius: 1.5,
                  colors: [
                Color(0xFF153E48),
                YhColors.abyss,
                Color(0xFF122236)
              ])),
          child: SafeArea(
              child: Column(children: [
            Material(
                color: Colors.transparent,
                child: Padding(
                  padding:
                      const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
                  child: Row(children: [
                    const Icon(Icons.auto_awesome,
                        color: YhColors.starlight, size: 18),
                    const SizedBox(width: 8),
                    const Expanded(
                        child: Text('前端演示 · 示例数据',
                            style:
                                TextStyle(fontSize: 12, color: YhColors.haze))),
                    TextButton(onPressed: onReset, child: const Text('重置演示')),
                  ]),
                )),
            Expanded(child: LayoutBuilder(builder: (context, constraints) {
              final width = math.min(constraints.maxWidth, 480.0);
              return Center(
                  child: SizedBox(
                      width: width,
                      child: ClipRRect(
                        borderRadius: BorderRadius.circular(
                            constraints.maxWidth > 500 ? 28 : 0),
                        child: MediaQuery(
                            data: MediaQuery.of(context).copyWith(
                                size: Size(width, constraints.maxHeight)),
                            child: child ?? const SizedBox()),
                      )));
            })),
          ])),
        ),
      );
}
