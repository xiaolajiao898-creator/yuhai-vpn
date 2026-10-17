import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';

/// GET /v1/me 首页聚合（方案 11.4）。阶段一先原样返回 Map，第 3–4 周随 freezed 模型替换。
final meProvider = FutureProvider.autoDispose<Map<String, dynamic>>((ref) => ref.watch(apiClientProvider).call('GET', '/v1/me'));
