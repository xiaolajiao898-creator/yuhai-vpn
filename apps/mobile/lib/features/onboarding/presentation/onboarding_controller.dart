import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';
import '../../../core/storage/session_store.dart';
import '../../connection/presentation/connection_controller.dart';
import '../data/auth_api.dart';
import '../../nodes/data/nodes_api.dart';
import '../../nodes/data/node_selection.dart';

final authApiProvider = Provider<AuthApi>((ref) => AuthApi(
    ref.watch(apiClientProvider),
    ref.watch(deviceKeyProvider),
    ref.watch(sessionStoreProvider)));

/// 当前会话；null 表示未登录。启动时从安全存储读取。
final sessionProvider =
    AsyncNotifierProvider<SessionController, Session?>(SessionController.new);

class SessionController extends AsyncNotifier<Session?> {
  @override
  Future<Session?> build() {
    final store = ref.watch(sessionStoreProvider);
    final client = ref.read(apiClientProvider);
    bool disposed = false;
    void sessionLost(int _) {
      if (disposed) return;
      // ApiClient has already cleared the matching stored session. Publish the
      // loss immediately so the router cannot leave a revoked account open.
      _clearConnectionState();
      state = const AsyncData(null);
      final engine = ref.read(vpnEngineProvider);
      unawaited(engine.disconnect().catchError((Object _) {
        // The revoked session stays signed out even if disconnect reports an
        // engine error; do not resurrect it or leak an unhandled async error.
      }));
    }

    client.onSessionLost = sessionLost;
    ref.onDispose(() {
      disposed = true;
      if (identical(client.onSessionLost, sessionLost)) {
        client.onSessionLost = null;
      }
    });
    return store.session();
  }

  /// 注册：成功后保存会话并把账户密钥交给备份页（内存传递，不落盘）。
  Future<RegisterResult> register({InviteHint? invite}) async {
    final r = await ref.read(authApiProvider).register(invite: invite);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
    return r;
  }

  Future<void> restore(String secret) async {
    await ref.read(authApiProvider).restore(secret);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
  }

  Future<void> registerWithAddress(String address, String password,
      {InviteHint? invite}) async {
    await ref
        .read(authApiProvider)
        .registerWithAddress(address, password, invite: invite);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
  }

  Future<void> loginWithPassword(String address, String password) async {
    await ref.read(authApiProvider).loginWithPassword(address, password);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
  }

  Future<void> completePasswordReset(
      String challengeId, String newPassword) async {
    await ref
        .read(authApiProvider)
        .completePasswordReset(challengeId, newPassword);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
  }

  Future<void> replaceDevice(String ticket, String removeId) async {
    await ref.read(authApiProvider).replaceDevice(ticket, removeId);
    state = AsyncData(await ref.read(sessionStoreProvider).session());
  }

  void _clearConnectionState() {
    ref.invalidate(connectionControllerProvider);
    ref.invalidate(nodesProvider);
    ref.invalidate(selectedNodeProvider);
    ref.invalidate(latencyProvider);
    ref.invalidate(connectionFailureProvider);
  }

  Future<void> signOut() async {
    _clearConnectionState();
    await ref.read(sessionStoreProvider).clearSession();
    state = const AsyncData(null);
  }
}

/// 通过归因链（APK 签名块渠道 > 剪贴板 > 手动）得到的邀请码；阶段一先读剪贴板 `YHINVITE:XXXXXX`。
final inviteHintProvider = FutureProvider<InviteHint?>((ref) async => null);
