import 'package:flutter/foundation.dart' show kIsWeb;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/platform/platform_name.dart';
import '../features/nodes/data/node_selection.dart';
import '../features/connection/presentation/connection_controller.dart';
import '../features/update/update_flow.dart';
import 'desktop_frame.dart';
import 'router.dart';
import 'theme/theme.dart';

class YuhaiApp extends ConsumerWidget {
  const YuhaiApp({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    ref.watch(selectedNodeHydrationProvider);
    return MaterialApp.router(
      title: '宇海VPN',
      theme: buildDeepSeaTheme(),
      routerConfig: ref.watch(routerProvider),
      debugShowCheckedModeBanner: false,
      // 电脑上窗口拉宽、最大化时，界面排在居中的一栏里，不再贴左、拉满。
      // 米白底：状态栏用深色图标（有标题栏的页面由标题栏主题设置，同样是深色）。
      builder: (context, child) => AnnotatedRegion<SystemUiOverlayStyle>(
        value: SystemUiOverlayStyle.dark,
        child: DesktopFrame(
          enabled: !kIsWeb && isDesktop,
          child: UpdateScheduler(
            engine: ref.watch(vpnEngineProvider),
            child: child ?? const SizedBox.shrink(),
          ),
        ),
      ),
    );
  }
}
