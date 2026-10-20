import 'dart:async';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../core/network/api_client.dart';
import '../../../core/storage/session_store.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../../platform/vpn/native_vpn_engine.dart';
import '../../../platform/vpn/unavailable_vpn_engine.dart';
import '../../../platform/vpn/vpn_engine.dart';
import '../../../platform/vpn/windows_wireguard_engine.dart';
import '../../../platform/vpn/windows_xray_engine.dart';
import '../../home/data/me_api.dart';
import '../../nodes/data/auto_select.dart';
import '../../nodes/data/node_selection.dart';
import '../../nodes/data/node_shortcuts.dart';
import '../../nodes/data/nodes_api.dart';
import '../../nodes/data/node_probe.dart';
import '../../onboarding/presentation/onboarding_controller.dart';
import '../../settings/data/connection_preferences.dart';
import '../data/connection_api.dart';
import '../data/session_guard.dart';

final vpnDemoModeProvider = Provider<bool>((_) => false);

/// 没有可用的 WireGuard 节点时，Windows 不调用 WireGuard 引擎。
bool windowsWireGuardSelectable(Iterable<NodeItem> nodes) => nodes.any((node) =>
    node.available && !node.isEgress && node.protocols.contains('WIREGUARD'));

final vpnEngineProvider = Provider<VpnEngine>((ref) {
  if (kIsWeb) return UnavailableVpnEngine();
  switch (defaultTargetPlatform) {
    case TargetPlatform.windows:
      // 出口走随包的 Xray。WireGuard 引擎留着，但列表里没有 WireGuard 节点时不会被调用。
      final wireGuard = WindowsWireGuardEngine();
      final engine =
          WindowsXrayEngine(wireGuard: wireGuard, allowWireGuard: true);
      ref.onDispose(engine.dispose);
      return engine;
    case TargetPlatform.android:
    case TargetPlatform.iOS:
    case TargetPlatform.macOS:
      // macOS 与 iOS 共用同一套方法通道协议；原生隧道扩展未打包时会报「未启用 VPN 引擎」。
      final engine = NativeVpnEngine();
      ref.onDispose(engine.dispose);
      return engine;
    default:
      return UnavailableVpnEngine();
  }
});
final connectionFailureProvider = StateProvider<VpnFailure?>((_) => null);

/// 本次连接的如实提示（例如所选出口不支持 UDP，或「重连中 / 已切换线路 / 已保护」）。
final connectionNoticeProvider = StateProvider<String?>((_) => null);

/// 本地网络是否真的被拦住。只有引擎回执成功才为 true，界面才能说「已保护」。
final trafficHeldProvider = StateProvider<bool>((_) => false);

class ConnectionController extends Notifier<VpnStatus> {
  var _generation = 0;
  var _disposed = false;
  var _acceptEngineEvents = false;
  var _armed = false;
  var _userStop = false;
  var _suppressSelect = false;
  var _recovering = false;
  var _recoverQueued = false;
  var _badStreak = 0;
  var _failuresOnLine = 0;
  int? _epochSeen;
  Timer? _healthTimer;
  Timer? _networkTimer;

  @override
  VpnStatus build() {
    _disposed = false;
    _acceptEngineEvents = false;
    final engine = ref.watch(vpnEngineProvider);
    _generation++;
    var active = true;
    final sub = engine.status.listen((status) {
      if (!(active && !_disposed && _acceptEngineEvents)) return;
      state = status;
      if (status == VpnStatus.connected && engine.canRecover && !_userStop) {
        _failuresOnLine = 0;
        _badStreak = 0;
        _arm(engine);
      }
    });
    final failures = engine.failures.listen((failure) {
      if (!(active && !_disposed && _acceptEngineEvents)) return;
      ref.read(connectionFailureProvider.notifier).state = failure;
      if (failure.code == 'VPN_REVOKED' ||
          failure.code == 'NO_PLAN' ||
          failure.code == 'AUTH_REQUIRED' ||
          failure.code == 'ACCOUNT_DISABLED' ||
          failure.code == 'PERMISSION_DENIED') {
        if (failure.code == 'VPN_REVOKED') _userStop = true;
        _disarm();
        return;
      }
      if (_armed && !_userStop) {
        unawaited(_recover(networkChanged: false));
      } else if (!_userStop) {
        unawaited(_holdIfProtecting());
      }
    });
    ref.listen(selectedNodeProvider, (previous, next) {
      if (_suppressSelect) return;
      if (previous != next &&
          (state == VpnStatus.connecting || state == VpnStatus.reconnecting)) {
        _generation++;
        _acceptEngineEvents = false;
        state = VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('SELECTION_CHANGED', '线路选择已变化，请重新连接');
        unawaited(engine.disconnect().catchError((Object _) {}));
      }
    });
    // SessionController explicitly invalidates this controller on logout/revoke.
    // Listening back to sessionProvider would create a Riverpod dependency cycle.
    ref.onDispose(() {
      final nativeStarted = _acceptEngineEvents;
      active = false;
      _disposed = true;
      _generation++;
      _userStop = true;
      _disarm();
      _acceptEngineEvents = false;
      sub.cancel();
      failures.cancel();
      if (nativeStarted) {
        unawaited(engine.disconnect().catchError((Object _) {}));
      }
    });
    return VpnStatus.disconnected;
  }

  /// 没有线路，和没有套餐，是两件不同的事。后者引导去购买或兑换。
  Future<VpnFailure> _noLineOrPlan() async {
    final me = ref.read(meProvider).valueOrNull;
    final entitlement = me?['entitlement'];
    if (entitlement is Map && entitlement['blocked_reason'] == 'NO_PLAN') {
      return const VpnFailure('NO_PLAN', '没有可用套餐，请先购买或兑换套餐');
    }
    return const VpnFailure('NO_LINE', '暂时没有可用线路，请稍后刷新再试');
  }

  bool _sameSession(Session expected) {
    final actual = ref.read(sessionProvider).valueOrNull;
    return actual?.accountNo == expected.accountNo &&
        actual?.deviceId == expected.deviceId;
  }

  /// 出口连接：确认所选出口仍可用 → 系统授权 → 再次确认 → 现取连接材料 → 交给原生引擎。
  /// 每一步之后都核对「代次未变、选择未变、账户未变」，任何变化都放弃这次尝试。
  Future<void> _connectEgress(
      WireGuardVpnEngine engine, String id, Session session,
      {required bool Function() current,
      required bool Function() validAttempt}) async {
    bool usable(List<NodeItem> list) =>
        list.any((n) => n.id == id && n.isEgress && n.available);
    final nodes = await ref.refresh(nodesProvider.future);
    if (!current()) return;
    if (!validAttempt() || !usable(nodes)) {
      throw await _noLineOrPlan();
    }
    if (!await engine.prepare()) {
      if (current()) {
        state = VpnStatus.disconnected;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('PERMISSION_DENIED', '未获得连接权限');
      }
      return;
    }
    if (!current()) return;
    final fresh = await ref.refresh(nodesProvider.future);
    if (!current()) return;
    if (!validAttempt() || !usable(fresh)) {
      throw const FormatException('线路状态已变化');
    }
    final EgressProfile profile =
        await ref.read(connectionApiProvider).egressProfile(session, id);
    if (!current()) return;
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    ref.read(connectionNoticeProvider.notifier).state =
        profile.notice.isEmpty ? null : profile.notice;
    _acceptEngineEvents = true;
    await engine.connect(profile);
    _remember(id);
  }

  /// 记入「最近使用」：只记本机、只记编号；保存失败不影响连接。
  void _remember(String id) {
    unawaited(ref
        .read(nodeShortcutsProvider.notifier)
        .recordRecent(id)
        .catchError((Object _) {}));
  }

  void _disarm() {
    _armed = false;
    _badStreak = 0;
    _failuresOnLine = 0;
    _epochSeen = null;
    _healthTimer?.cancel();
    _networkTimer?.cancel();
    _healthTimer = null;
    _networkTimer = null;
  }

  void _arm(VpnEngine engine) {
    if (_armed || _disposed || !engine.canRecover || _userStop) return;
    _armed = true;
    _healthTimer?.cancel();
    _networkTimer?.cancel();
    _healthTimer = Timer.periodic(tunnelProbeInterval, (_) {
      unawaited(_checkTunnel());
    });
    _networkTimer = Timer.periodic(const Duration(seconds: 4), (_) {
      unawaited(_checkNetwork());
    });
  }

  ConnectionPreferences _preferences() =>
      ref.read(connectionPreferencesProvider).valueOrNull ??
      const ConnectionPreferences();

  Future<void> _holdIfProtecting() async {
    final engine = ref.read(vpnEngineProvider);
    if (!engine.canRecover || _userStop || _disposed) return;
    if (!_preferences().leakProtect) return;
    final held = await engine.holdTraffic();
    if (_disposed || _userStop) {
      if (held) await engine.releaseTraffic();
      return;
    }
    ref.read(trafficHeldProvider.notifier).state = held;
    ref.read(connectionNoticeProvider.notifier).state =
        held ? '已保护。流量已拦住，点「断开」才会恢复本地网络。' : '断网保护没有打开';
  }

  Future<void> _checkTunnel() async {
    if (!_armed || _disposed || _userStop || _recovering) return;
    if (state != VpnStatus.connected) return;
    final engine = ref.read(vpnEngineProvider);
    final latency = await engine.probeTunnel();
    if (!_armed || _disposed || _userStop || state != VpnStatus.connected) {
      return;
    }
    if (tunnelSampleBad(latency)) {
      _badStreak++;
      if (_badStreak >= tunnelBadStreak) {
        _badStreak = 0;
        await _recover(networkChanged: false);
      }
    } else {
      _badStreak = 0;
    }
  }

  Future<void> _checkNetwork() async {
    if (!_armed || _disposed || _userStop || _recovering) return;
    if (state != VpnStatus.connected && state != VpnStatus.reconnecting) return;
    final epoch = await ref.read(vpnEngineProvider).networkEpoch();
    if (!_armed || _disposed || _userStop) return;
    final seen = _epochSeen;
    _epochSeen = epoch;
    if (seen != null && epoch != seen) {
      await _recover(networkChanged: true);
    }
  }

  Future<void> _recover({required bool networkChanged}) async {
    if (_disposed || _userStop || !_armed) return;
    if (_recovering) {
      _recoverQueued = true;
      return;
    }
    final engine = ref.read(vpnEngineProvider);
    if (!engine.canRecover) return;
    _recovering = true;
    final generation = _generation;
    try {
      final prefs = _preferences();
      // 节点列表没人监听时会被释放。重连时重新读取，才能在同一国家里换线。
      List<NodeItem> lines = const [];
      try {
        lines = await ref.read(nodesProvider.future);
      } catch (_) {}
      if (_disposed || _userStop || generation != _generation) return;
      final currentId = ref.read(selectedNodeProvider);
      final current = lines.where((node) => node.id == currentId).firstOrNull;
      _failuresOnLine = networkChanged ? 1 : _failuresOnLine + 1;
      final decision = decideRecovery(
        leakProtect: prefs.leakProtect,
        autoRecover: prefs.autoRecover,
        networkChanged: networkChanged,
        failuresOnLine: _failuresOnLine,
        current: current,
        lines: lines,
      );
      var held = false;
      if (decision.protected) held = await engine.holdTraffic();
      if (_disposed || _userStop || generation != _generation) {
        if (held && _userStop) await engine.releaseTraffic();
        return;
      }
      ref.read(trafficHeldProvider.notifier).state = held;
      ref.read(connectionNoticeProvider.notifier).state = protectionNotice(
          decision.notice,
          wanted: decision.protected,
          held: held);
      if (decision.action == RecoveryAction.release) {
        _disarm();
        ref.read(trafficHeldProvider.notifier).state = false;
        await engine.releaseTraffic();
        await engine.disconnect();
        if (!_disposed && generation == _generation) {
          state = VpnStatus.disconnected;
        }
        return;
      }
      _acceptEngineEvents = false;
      state = VpnStatus.reconnecting;
      if (decision.action == RecoveryAction.hold) return;
      if (decision.delay > Duration.zero) {
        await Future<void>.delayed(decision.delay);
      }
      if (_disposed || _userStop || generation != _generation) return;
      if (decision.action == RecoveryAction.switchLine &&
          decision.lineId != null &&
          decision.lineId != currentId) {
        _suppressSelect = true;
        ref.read(selectedNodeProvider.notifier).state = decision.lineId;
        _suppressSelect = false;
        _failuresOnLine = 0;
      }
      await _drive(userInitiated: false);
    } finally {
      _recovering = false;
      final code = ref.read(connectionFailureProvider)?.code;
      const fatal = {
        'VPN_REVOKED',
        'NO_PLAN',
        'AUTH_REQUIRED',
        'ACCOUNT_DISABLED',
        'PERMISSION_DENIED'
      };
      if (fatal.contains(code)) _disarm();
      if (_recoverQueued && _armed && !_userStop && !_disposed) {
        _recoverQueued = false;
        unawaited(_recover(networkChanged: false));
      } else {
        _recoverQueued = false;
      }
    }
  }

  /// 用户点「断开」：放开本地网络。重连不会走这里。
  Future<void> release() => _userRelease();

  Future<void> _userRelease() async {
    final engine = ref.read(vpnEngineProvider);
    if (state == VpnStatus.disconnecting) return;
    final generation = ++_generation;
    bool current() => !_disposed && generation == _generation;
    _userStop = true;
    _disarm();
    _acceptEngineEvents = false;
    state = VpnStatus.disconnecting;
    ref.read(trafficHeldProvider.notifier).state = false;
    try {
      await engine.releaseTraffic();
      await engine.disconnect();
    } catch (_) {
      if (current()) {
        state = VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('DISCONNECT_FAILED', '断开未获确认，请重试');
      }
      return;
    }
    if (current()) {
      state = VpnStatus.disconnected;
      ref.read(connectionNoticeProvider.notifier).state = null;
    }
  }

  Future<void> toggle() => _drive(userInitiated: true);

  Future<void> _drive({required bool userInitiated}) async {
    final engine = ref.read(vpnEngineProvider);
    if (state == VpnStatus.disconnecting) return;
    if (userInitiated &&
        (state == VpnStatus.connected ||
            state == VpnStatus.connecting ||
            state == VpnStatus.reconnecting)) {
      await _userRelease();
      return;
    }
    final generation = ++_generation;
    bool current() => !_disposed && generation == _generation;
    _userStop = false;
    if (userInitiated) {
      ref.read(connectionFailureProvider.notifier).state = null;
      ref.read(connectionNoticeProvider.notifier).state = null;
      ref.read(trafficHeldProvider.notifier).state = false;
    }
    final demo = ref.read(vpnDemoModeProvider);
    if (!demo && engine is! WireGuardVpnEngine) {
      state = VpnStatus.error;
      ref.read(connectionFailureProvider.notifier).state =
          const VpnFailure('ENGINE_UNAVAILABLE', '当前平台暂不支持 VPN 连接');
      return;
    }
    final selected = ref.read(selectedNodeProvider);
    // 出口（e 开头）走 Xray 内核，安卓与 Windows 可用。用户点选时直接连；
    // 自动选择只在没有可探测的 WireGuard 节点时才回落到出口。
    final egress = !demo && selected != null && selected.startsWith('e');
    if (egress && !egressPlatformSupported) {
      state = VpnStatus.error;
      ref.read(connectionFailureProvider.notifier).state =
          const VpnFailure('ENGINE_UNAVAILABLE', '当前设备暂不支持该线路类型');
      return;
    }
    state = VpnStatus.connecting;
    await engine.applyPolicy(leakProtect: _preferences().leakProtect);
    if (!current()) return;
    Session? session;
    bool validAttempt() =>
        current() &&
        ref.read(selectedNodeProvider) == selected &&
        (demo || (session != null && _sameSession(session)));
    try {
      if (!demo) {
        session = await ref.read(sessionProvider.future);
        if (!current()) return;
        if (session == null) throw const VpnFailure('AUTH_REQUIRED', '请先登录账户');
        final preferences =
            await ref.read(connectionPreferencesProvider.future);
        if (!current()) return;
        if (!egress &&
            preferences.protocol != ProtocolPreference.auto &&
            preferences.protocol != ProtocolPreference.wireguard) {
          throw const VpnFailure('PROTOCOL_UNSUPPORTED',
              '当前版本支持 WireGuard，请在连接设置中选择自动或 WireGuard');
        }
        if (!egress && defaultTargetPlatform == TargetPlatform.windows) {
          final listed = await ref.refresh(nodesProvider.future);
          if (!current()) return;
          if (!windowsWireGuardSelectable(listed)) {
            throw const VpnFailure('ENGINE_UNAVAILABLE', '请先选择一条出口线路');
          }
        }
      }
      if (egress) {
        await _connectEgress(engine as WireGuardVpnEngine, selected, session!,
            current: current, validAttempt: validAttempt);
        return;
      }
      final protocol = demo ? 'MOCK' : 'WIREGUARD';
      final nodes = await ref.refresh(nodesProvider.future);
      if (!current()) return;
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      final supported = nodes
          .where((n) => n.available && n.protocols.contains(protocol))
          .toList();
      NodeItem? node;
      if (selected != null) {
        node = supported.where((n) => n.id == selected).firstOrNull;
        if (node == null || node.probeHost == null || !validAttempt()) {
          throw await _noLineOrPlan();
        }
      } else {
        final probeable = supported
            .where((n) => n.probeHost != null && n.probePort != null)
            .toList();
        if (probeable.isNotEmpty) {
          final latencies = await probeNodes(
              probeable, (host, port) => engine.probe(host, port),
              isCurrent: validAttempt);
          if (!current()) return;
          node = fastestProbed(probeable, latencies);
          if (node == null || !validAttempt()) {
            throw const VpnFailure('NO_LINE', '没有测到可用线路，请手动选择一条');
          }
        } else if (!demo &&
            egressPlatformSupported &&
            engine is WireGuardVpnEngine) {
          final egressNode = preferredEgress(nodes);
          if (egressNode == null || !validAttempt()) {
            throw await _noLineOrPlan();
          }
          await _connectEgress(engine, egressNode.id, session!,
              current: current, validAttempt: validAttempt);
          return;
        } else {
          throw await _noLineOrPlan();
        }
      }
      if (!await engine.prepare()) {
        if (current()) {
          state = VpnStatus.disconnected;
          ref.read(connectionFailureProvider.notifier).state =
              const VpnFailure('PERMISSION_DENIED', '未获得连接权限');
        }
        return;
      }
      if (!current()) return;
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      final fresh = await ref.refresh(nodesProvider.future);
      if (!current()) return;
      final confirmed = fresh
          .where((n) =>
              n.id == node!.id && n.available && n.protocols.contains(protocol))
          .firstOrNull;
      if (confirmed == null || confirmed.probeHost == null || !validAttempt()) {
        throw const FormatException('线路状态已变化');
      }
      ConnectProfile profile;
      if (demo) {
        profile = ConnectProfile(
            nodeId: confirmed.id,
            protocol: 'MOCK',
            server: confirmed.probeHost!);
      } else {
        final native = engine as WireGuardVpnEngine;
        final key = await native.publicKey(vpnIdentity(session!));
        if (!current()) return;
        if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
        final api = ref.read(connectionApiProvider);
        await api.uploadPublicKey(session, key);
        if (!current()) return;
        if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
        profile = await api.profile(session, confirmed.id, key);
        if (!current()) return;
        if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      }
      _acceptEngineEvents = true;
      await engine.connect(profile);
      _remember(confirmed.id);
    } catch (error) {
      if (!current()) return;
      _acceptEngineEvents = false;
      state = VpnStatus.error;
      ref.read(connectionFailureProvider.notifier).state = switch (error) {
        VpnFailure failure => failure,
        ApiException e => switch (e.code) {
            40101 ||
            40103 ||
            40107 ||
            40108 =>
              const VpnFailure('AUTH_REQUIRED', '登录或设备状态已变化，请重新登录'),
            40301 => const VpnFailure('NO_PLAN', '没有可用套餐，请先购买或兑换套餐'),
            40302 => const VpnFailure('TIER', '当前套餐等级不能使用该线路'),
            40303 => const VpnFailure('QUOTA_EXHAUSTED', '流量已用完，请补充流量后重试'),
            40305 => const VpnFailure('ACCOUNT_DISABLED', '账户暂不可用，请联系客服'),
            50304 =>
              const VpnFailure('ADDRESS_POOL_EXHAUSTED', '线路资源暂时不足，请稍后重试'),
            _ => VpnFailure(
                'REQUEST_FAILED', e.message.isEmpty ? '连接失败，请重试' : e.message),
          },
        FormatException e => VpnFailure('NODE_UNAVAILABLE',
            e.message.isEmpty ? '线路暂不可用，请刷新后重新选择' : e.message),
        _ => const VpnFailure('CONNECT_FAILED', '连接失败，请重试'),
      };
    }
  }
}

final connectionControllerProvider =
    NotifierProvider<ConnectionController, VpnStatus>(ConnectionController.new);
