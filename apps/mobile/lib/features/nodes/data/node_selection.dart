import 'package:flutter_riverpod/flutter_riverpod.dart';

/// Selection is a preference, never a permission to reuse stale connection data.
final selectedNodeProvider = StateProvider<String?>((_) => null);
final latencyProvider = StateProvider<Map<String, int?>>((_) => {});
