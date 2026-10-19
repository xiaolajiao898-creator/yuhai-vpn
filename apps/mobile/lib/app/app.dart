import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../features/nodes/data/node_selection.dart';
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
    );
  }
}
