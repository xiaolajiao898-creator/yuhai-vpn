import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/log/client_log.dart';
import '../core/network/api_client.dart';
import '../features/onboarding/presentation/onboarding_controller.dart';
import 'providers.dart';

/// 仅当进程环境有 YH_SMOKE_REGISTER=1 时由 main 调用。
/// 用一个不可能通过校验的地址打一次注册接口，确认请求能到服务器，并且不会开户。
Future<int> runRegisterSmoke() async {
  final container = ProviderContainer();
  try {
    final base = checkedApiBaseUrl();
    await ClientLog.record('smoke base=$base');
    final api = container.read(authApiProvider);
    await api.registerWithAddress('TINVALID', 'password1');
    await ClientLog.record('smoke unexpected success');
    return 2;
  } on ApiException catch (error, stack) {
    await ClientLog.exception('smoke api', error, stack);
    return 0;
  } on StateError catch (error, stack) {
    await ClientLog.exception('smoke config', error, stack);
    return 3;
  } catch (error, stack) {
    await ClientLog.exception('smoke other', error, stack);
    return 4;
  } finally {
    container.dispose();
  }
}
