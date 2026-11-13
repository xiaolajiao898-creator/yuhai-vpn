import 'dart:async';
import 'package:dio/dio.dart';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../core/network/api_client.dart';
import '../../../core/storage/session_store.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../../platform/vpn/native_vpn_engine.dart';
import '../../../platform/vpn/tunnel_diagnostic.dart';
import '../../../platform/vpn/tunnel_host_pins.dart';
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
      // tunnelHosts：断网保护拦着时系统查不了域名，用之前查到的服务器地址（见 tunnel_host_pins.dart）。
      final engine = WindowsXrayEngine(
          wireGuard: wireGuard,
          allowWireGuard: true,
          tunnelHosts: TunnelHostMemory());
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

/// 重连也解决不了的失败：账户、套餐、权限、系统撤销。遇到就停止自动重连；
/// 断网保护开着时流量继续拦着，直到用户点「断开」。
const _fatalFailures = {
  'VPN_REVOKED',
  'NO_PLAN',
  'AUTH_REQUIRED',
  'ACCOUNT_DISABLED',
  'PERMISSION_DENIED',
  'QUOTA_EXHAUSTED',
  // 设备数已满（自建线路的设备上限，或批发侧拒绝第 4 条 peer）：换线重试也不会好，
  // 继续重连只会反复打批发接口。
  'DEVICE_LIMIT',
};

class ConnectionController extends Notifier<VpnStatus> {
  var _generation = 0;
  var _disposed = false;
  var _acceptEngineEvents = false;

  /// 探测失败后的试连已经把配置交给引擎，但握手结果还没回来。界面销毁时也要拆隧道。
  var _connectInFlight = false;

  /// 本设备可能还占着的 WorldVPN peer（w 开头的线路编号）。用户断开、改连非批发线路、
  /// 自动重连放弃时释放。同一设备改连另一条批发线路时，服务端先删旧 peer 再建新的，
  /// 一台设备最多一条（数据库唯一索引），所以重连不会叠加占用 3 台的名额。
  String? _wholesaleLine;

  /// 界面重开时接手了原生侧仍在的隧道/黑洞，不知道是不是批发线路：断开时也释放一次。
  var _wholesaleUnknown = false;
  var _armed = false;
  var _userStop = false;
  var _suppressSelect = false;
  var _recovering = false;
  var _recoverQueued = false;
  var _badStreak = 0;
  var _failuresOnLine = 0;

  /// 用户每次操作（连接、断开、改选线路）加一。自动重连只在它没变时才接着重试。
  var _userEpoch = 0;

  /// 这一轮自动重连里已经换走过的线路：换线时先挑还没试过的，全试过才回头。
  final _triedLines = <String>{};

  /// 上次读到的线路列表。重连时取不到新列表（网络正坏着）也能在同一国家里换线。
  List<NodeItem> _lastLines = const [];

  /// 记下最新的线路列表。Windows 上顺带把各线路的探测地址交给引擎：断网保护拦着、
  /// 系统查不到域名时，重连换到以前没连过的线路也有地址可用（见 tunnel_host_pins.dart）。
  void _rememberLines(List<NodeItem> lines) {
    _lastLines = lines;
    final engine = ref.read(vpnEngineProvider);
    if (engine is WindowsXrayEngine) {
      engine.noteLineHosts({
        for (final n in lines)
          if (n.available && n.probeHost != null) n.id: n.probeHost!,
      });
    }
  }

  int? _epochSeen;
  Timer? _healthTimer;
  Timer? _networkTimer;

  @override
  VpnStatus build() {
    _disposed = false;
    _acceptEngineEvents = false;
    _connectInFlight = false;
    final engine = ref.watch(vpnEngineProvider);
    _generation++;
    var active = true;
    final sub = engine.status.listen((status) {
      if (!(active && !_disposed && _acceptEngineEvents)) return;
      state = status;
      if (status == VpnStatus.connected && engine.canRecover && !_userStop) {
        _failuresOnLine = 0;
        _badStreak = 0;
        _triedLines.clear();
        // 连上之后重新取网络基线：拆旧隧道、建新隧道本身会让系统网络回调动一下。
        _epochSeen = null;
        _arm(engine);
      }
    });
    final failures = engine.failures.listen((failure) {
      if (!active || _disposed) return;
      if (!_acceptEngineEvents) {
        // 拦着流量期间系统或别的 VPN 应用收走了 VPN：保护已经不在，如实改口，也不再重连。
        if (failure.code == 'VPN_REVOKED' && ref.read(trafficHeldProvider)) {
          _userStop = true;
          _disarm();
          ref.read(trafficHeldProvider.notifier).state = false;
          ref.read(connectionFailureProvider.notifier).state = failure;
          ref.read(connectionNoticeProvider.notifier).state =
              'VPN 已被系统或其他 VPN 应用接管，断网保护已失效';
          state = VpnStatus.error;
        }
        return;
      }
      ref.read(connectionFailureProvider.notifier).state = failure;
      // 这次连接到此为止。原生侧和 Dart 侧的握手超时可能各报一次，只处理第一次，
      // 免得同一次掉线触发两轮重连。下一次连接会重新打开事件。
      _acceptEngineEvents = false;
      state = VpnStatus.error;
      if (_fatalFailures.contains(failure.code)) {
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
        _userEpoch++;
        _acceptEngineEvents = false;
        _disarm();
        state = VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('SELECTION_CHANGED', '线路选择已变化，请重新连接');
        if (_preferences().leakProtect && engine.canRecover) {
          // 改选线路不等于「断开」：断网保护开着时继续拦住，等用户点连接或「断开并恢复本地网络」。
          unawaited(_holdIfProtecting());
        } else {
          unawaited(_releaseWholesale());
          unawaited(engine.disconnect().catchError((Object _) {}));
        }
      }
    });
    // SessionController explicitly invalidates this controller on logout/revoke.
    // Listening back to sessionProvider would create a Riverpod dependency cycle.
    ref.onDispose(() {
      final nativeStarted = _acceptEngineEvents || _connectInFlight;
      active = false;
      _disposed = true;
      _generation++;
      _userStop = true;
      _disarm();
      _acceptEngineEvents = false;
      sub.cancel();
      failures.cancel();
      if (nativeStarted) {
        unawaited(_releaseWholesale());
        unawaited(engine.disconnect().catchError((Object _) {}));
      }
    });
    unawaited(_adoptNativeSession(engine));
    return VpnStatus.disconnected;
  }

  /// 安卓：从最近任务划掉界面不会断开 VPN（隧道由系统 VpnService 承载），断网保护的黑洞接口
  /// 也可能还在。新界面打开时读一次原生状态，免得显示「未连接」却拦着网络，或已连着却没法断开。
  /// 旧界面的原生事件属于旧代次，新界面收不到；接手的隧道靠周期探测发现掉线。
  Future<void> _adoptNativeSession(VpnEngine engine) async {
    if (engine is! NativeVpnEngine) return;
    final generation = _generation;
    final native = await engine.activeState();
    if (native == null ||
        _disposed ||
        generation != _generation ||
        state != VpnStatus.disconnected) {
      return;
    }
    if (native.tunnel) {
      _wholesaleUnknown = true;
      _userStop = false;
      state = native.connected ? VpnStatus.connected : VpnStatus.reconnecting;
      _arm(engine);
    } else if (native.held) {
      _wholesaleUnknown = true;
      ref.read(trafficHeldProvider.notifier).state = true;
      ref.read(connectionNoticeProvider.notifier).state =
          '上次的连接已经断开，断网保护仍在拦住流量。点连接重新连上，或点「断开并恢复本地网络」。';
      state = VpnStatus.error;
    }
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

  /// 延迟全部测失败时，换下一条仍可能连上：账户、套餐、权限这类错误换线没有用。
  bool _keepTryingLines(Object error) {
    if (error is Error) return false;
    if (error is VpnFailure) {
      return !_fatalFailures.contains(error.code) &&
          error.code != 'ENGINE_UNAVAILABLE' &&
          error.code != 'PERMISSION_DENIED';
    }
    if (error is ApiException) {
      return switch (error.code) {
        40101 ||
        40103 ||
        40107 ||
        40108 ||
        40301 ||
        40303 ||
        40305 ||
        40902 =>
          false,
        _ => true,
      };
    }
    if (error is FormatException) {
      return error.message != '连接选择或账户已变化' &&
          error.message != 'VPN 身份无效' &&
          error.message != 'VPN 设备公钥无效';
    }
    if (error is DioException) return isConnectTimeout(error);
    return true;
  }

  /// 探测端口全部连不上时，按服务端返回的顺序直接试连，失败再试下一条，最多 3 条。
  /// 不再弹出「没有测到可用线路」。能报告握手结果的引擎会等到连上或失败再换下一条。
  Future<void> _connectProbeFallback(
    List<NodeItem> ordered, {
    required VpnEngine engine,
    required Session? session,
    required String protocol,
    required bool demo,
    required bool userInitiated,
    required bool Function() current,
    required bool Function() validAttempt,
  }) async {
    final lines = ordered.take(3).toList();
    if (lines.isEmpty) throw await _noLineOrPlan();
    if (!await engine.prepare()) {
      if (current()) {
        state = userInitiated ? VpnStatus.disconnected : VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('PERMISSION_DENIED', '未获得连接权限');
        unawaited(_reportConnectFailure(
            const VpnFailure('PERMISSION_DENIED', '未获得连接权限')));
      }
      return;
    }
    if (!current()) return;
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final fresh = await ref.refresh(nodesProvider.future);
    if (!current()) return;
    _rememberLines(fresh);
    Object? lastError;
    for (var i = 0; i < lines.length; i++) {
      try {
        await _openFallbackLine(
          fresh: fresh,
          chosen: lines[i],
          engine: engine,
          session: session,
          protocol: protocol,
          demo: demo,
          current: current,
          validAttempt: validAttempt,
        );
        return;
      } on _AttemptAborted {
        return;
      } catch (error) {
        if (!current()) return;
        _acceptEngineEvents = false;
        try {
          await engine.disconnect();
        } catch (_) {}
        if (!current()) return;
        if (userInitiated || !_armed) await _releaseWholesale();
        if (!current()) return;
        lastError = error;
        if (i == lines.length - 1 || !_keepTryingLines(error)) break;
        state = VpnStatus.connecting;
        ref.read(connectionFailureProvider.notifier).state = null;
        ref.read(connectionNoticeProvider.notifier).state = null;
      }
    }
    throw lastError ?? const VpnFailure('CONNECT_FAILED', '连接失败，请重试');
  }

  Future<void> _openFallbackLine({
    required List<NodeItem> fresh,
    required NodeItem chosen,
    required VpnEngine engine,
    required Session? session,
    required String protocol,
    required bool demo,
    required bool Function() current,
    required bool Function() validAttempt,
  }) async {
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final confirmed = fresh
        .where((n) =>
            n.id == chosen.id && n.available && n.protocols.contains(protocol))
        .firstOrNull;
    if (confirmed == null || confirmed.probeHost == null) {
      throw const FormatException('线路状态已变化');
    }
    if (!demo && confirmed.id.startsWith('w')) {
      if (!worldvpnPlatformSupported) {
        throw const VpnFailure('ENGINE_UNAVAILABLE', '当前设备暂不支持该线路类型');
      }
      final profile = await ref
          .read(connectionApiProvider)
          .worldvpnProfile(session!, confirmed.id);
      if (!current()) throw const _AttemptAborted();
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      _wholesaleLine = confirmed.id;
      _wholesaleUnknown = false;
      await _finishFallbackConnect(engine, profile, current);
      _remember(confirmed.id);
      return;
    }
    final ConnectProfile profile;
    if (demo) {
      profile = ConnectProfile(
          nodeId: confirmed.id, protocol: 'MOCK', server: confirmed.probeHost!);
    } else {
      await _leaveWholesale();
      if (!current()) throw const _AttemptAborted();
      if (engine is! WireGuardVpnEngine) {
        throw const VpnFailure('ENGINE_UNAVAILABLE', '当前平台暂不支持 VPN 连接');
      }
      final key = await engine.publicKey(vpnIdentity(session!));
      if (!current()) throw const _AttemptAborted();
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      final api = ref.read(connectionApiProvider);
      await api.uploadPublicKey(session, key);
      if (!current()) throw const _AttemptAborted();
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      profile = await api.profile(session, confirmed.id, key);
      if (!current()) throw const _AttemptAborted();
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    }
    await _finishFallbackConnect(engine, profile, current);
    _remember(confirmed.id);
  }

  /// 配置被引擎收下之后，还要等握手。30 秒内没连上就交给外层换下一条。
  Future<void> _finishFallbackConnect(
      VpnEngine engine, ConnectProfile profile, bool Function() current) async {
    final watch = _HandshakeWatch(engine);
    _connectInFlight = true;
    try {
      if (!engine.canRecover) _acceptEngineEvents = true;
      await engine.connect(profile);
      if (!current()) throw const _AttemptAborted();
      if (_waitsForHandshake(engine)) {
        final failure = await watch.untilSettled(
          timeout: const Duration(seconds: 32),
          isCurrent: current,
        );
        if (failure != null) throw failure;
        _acceptEngineEvents = true;
        _failuresOnLine = 0;
        _badStreak = 0;
        _triedLines.clear();
        _epochSeen = null;
        state = VpnStatus.connected;
        if (!_userStop && engine.canRecover) _arm(engine);
        return;
      }
      await Future<void>.delayed(Duration.zero);
      if (!current()) throw const _AttemptAborted();
      final failure = watch.failure;
      if (failure != null) throw failure;
    } finally {
      _connectInFlight = false;
      await watch.close();
    }
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
      {required bool userInitiated,
      required bool Function() current,
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
        // 自动重连途中流量可能正拦着：不能显示成「未连接」，否则界面上没有恢复网络的按钮。
        state = userInitiated ? VpnStatus.disconnected : VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('PERMISSION_DENIED', '未获得连接权限');
        unawaited(_reportConnectFailure(
            const VpnFailure('PERMISSION_DENIED', '未获得连接权限')));
      }
      return;
    }
    if (!current()) return;
    final fresh = await ref.refresh(nodesProvider.future);
    if (!current()) return;
    if (!validAttempt() || !usable(fresh)) {
      throw const FormatException('线路状态已变化');
    }
    _rememberLines(fresh);
    await _leaveWholesale();
    if (!current()) return;
    final EgressProfile profile =
        await ref.read(connectionApiProvider).egressProfile(session, id);
    if (!current()) return;
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final notice = ref.read(connectionNoticeProvider.notifier);
    if (userInitiated) {
      notice.state = profile.notice.isEmpty ? null : profile.notice;
    } else if (profile.notice.isNotEmpty) {
      // 重连时保留「已切换线路 / 已保护」，出口自己的提示接在后面。
      final previous = notice.state;
      notice.state = previous == null || previous.isEmpty
          ? profile.notice
          : '$previous · ${profile.notice}';
    }
    _acceptEngineEvents = true;
    await engine.connect(profile);
    _remember(id);
  }

  /// 释放本设备的 WorldVPN peer。尽力而为：失败时下次连批发线路时服务端会先删旧的，
  /// 或由 worker 在吊销设备时补删。先清标记，避免同一条被释放两次。
  Future<void> _releaseWholesale() async {
    final line = _wholesaleLine;
    final unknown = _wholesaleUnknown;
    _wholesaleLine = null;
    _wholesaleUnknown = false;
    if (line == null && !unknown) return;
    if (!worldvpnPlatformSupported) return;
    final session = ref.read(sessionProvider).valueOrNull;
    if (session == null) return;
    final api = ref.read(connectionApiProvider);
    try {
      await api.releaseWorldVPN(session).timeout(const Duration(seconds: 15));
    } catch (_) {}
  }

  /// 改连非批发线路（出口、自建节点）之前，先把还占着的批发 peer 还掉，不占 3 台的名额。
  Future<void> _leaveWholesale() async {
    if (_wholesaleLine == null && !_wholesaleUnknown) return;
    await _releaseWholesale();
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
    _triedLines.clear();
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

  /// 断线自检：大约每 20 秒经隧道探测一次，连续两次失败就重连。
  /// 引擎自己报「重连中」（例如 WireGuard 三分钟没有新握手）时也接着探测，一直不通就由这里接手。
  Future<void> _checkTunnel() async {
    bool watching() =>
        _armed &&
        !_disposed &&
        !_userStop &&
        !_recovering &&
        (state == VpnStatus.connected || state == VpnStatus.reconnecting);
    if (!watching()) return;
    final engine = ref.read(vpnEngineProvider);
    final latency = await engine.probeTunnel();
    if (!watching()) return;
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
    if (!_armed || _disposed || _userStop || _recovering) return;
    final seen = _epochSeen;
    _epochSeen = epoch;
    if (seen != null && epoch != seen) {
      await _recover(networkChanged: true);
    }
  }

  /// 掉线后自动重连。顺序：先拦住流量（断网保护开着时）→ 读线路 → 决定重试当前线路还是
  /// 换同一国家/地区的另一条 → 退避等待 → 重新取连接材料并连接。没连上就按退避继续重试，
  /// 直到连上、遇到重连也解决不了的失败、或用户自己操作。
  Future<void> _recover({required bool networkChanged}) async {
    if (_disposed || _userStop || !_armed) return;
    if (_recovering) {
      _recoverQueued = true;
      return;
    }
    final engine = ref.read(vpnEngineProvider);
    if (!engine.canRecover) return;
    _recovering = true;
    // 旧隧道接下来的事件（拆接口、黑洞接管）都不算数，等新的连接把它重新打开。
    _acceptEngineEvents = false;
    final generation = _generation;
    final userEpoch = _userEpoch;
    var retry = false;
    try {
      final prefs = _preferences();
      // 先拦住再做别的：隧道死掉时，本应用发往服务端的请求也可能卡在里面。黑洞接口（安卓）/
      // 防火墙规则（Windows）接管之后，其他程序的流量被拦住，本应用才能直连服务端取新的连接材料。
      var held = false;
      if (prefs.leakProtect) {
        held = await engine.holdTraffic();
      } else {
        // 断网保护关着：先拆掉死掉的隧道，否则本应用去服务端取新连接材料的请求也会卡在里面
        // （安卓 WireGuard 隧道包含本应用；Windows 的 WireGuard 服务自带防火墙）。
        // 这段时间流量走本地网络，这是用户关掉断网保护时选择的行为。
        try {
          await engine.disconnect();
        } catch (_) {}
      }
      if (_disposed || _userStop || generation != _generation) {
        if (held && _userStop) await engine.releaseTraffic();
        return;
      }
      ref.read(trafficHeldProvider.notifier).state = held;
      // 每次连接都会刷新一次线路列表并记在 _lastLines，这里直接用；还没有时才去读。
      // （批发线路列表每次都要问 WorldVPN，服务端按设备每分钟限 20 次，重连时不要多读。）
      var lines = _lastLines;
      if (lines.isEmpty) {
        try {
          lines = await ref
              .read(nodesProvider.future)
              .timeout(const Duration(seconds: 20));
          _rememberLines(lines);
        } catch (_) {}
      }
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
        avoid: _triedLines,
      );
      ref.read(connectionNoticeProvider.notifier).state = protectionNotice(
          decision.notice,
          wanted: decision.protected,
          held: held);
      if (decision.action == RecoveryAction.release) {
        _disarm();
        ref.read(trafficHeldProvider.notifier).state = false;
        try {
          await engine.releaseTraffic();
          await engine.disconnect();
        } catch (_) {}
        unawaited(_releaseWholesale());
        if (!_disposed && generation == _generation) {
          state = VpnStatus.disconnected;
        }
        return;
      }
      state = VpnStatus.reconnecting;
      if (decision.action == RecoveryAction.hold) {
        // 没开自动重连：拦着就好，不再探测、不再重连，等用户点「断开」或重新连接。
        _disarm();
        return;
      }
      if (decision.delay > Duration.zero) {
        await Future<void>.delayed(decision.delay);
      }
      if (_disposed || _userStop || generation != _generation) return;
      if (decision.action == RecoveryAction.switchLine &&
          decision.lineId != null &&
          decision.lineId != currentId) {
        if (currentId != null) _triedLines.add(currentId);
        _suppressSelect = true;
        ref.read(selectedNodeProvider.notifier).state = decision.lineId;
        _suppressSelect = false;
        _failuresOnLine = 0;
      }
      await _drive(userInitiated: false);
      // 取连接材料、授权等环节失败时引擎不会再发事件；没有这一步，自动重连会停在「连接失败」。
      retry = state == VpnStatus.error;
    } finally {
      _recovering = false;
      final code = _disposed ? null : ref.read(connectionFailureProvider)?.code;
      if (_fatalFailures.contains(code)) {
        _disarm();
        retry = false;
        unawaited(_releaseWholesale());
      }
      final again = (_recoverQueued || retry) &&
          _armed &&
          !_userStop &&
          !_disposed &&
          userEpoch == _userEpoch;
      _recoverQueued = false;
      if (again) unawaited(_recover(networkChanged: false));
    }
  }

  /// 用户点「断开」：放开本地网络。重连不会走这里。
  Future<void> release() => _userRelease();

  Future<void> _userRelease() async {
    final engine = ref.read(vpnEngineProvider);
    if (state == VpnStatus.disconnecting) return;
    final generation = ++_generation;
    _userEpoch++;
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
      unawaited(_releaseWholesale());
      if (current()) {
        state = VpnStatus.error;
        ref.read(connectionFailureProvider.notifier).state =
            const VpnFailure('DISCONNECT_FAILED', '断开未获确认，请重试');
      }
      return;
    }
    // 隧道拆掉之后再还批发 peer：这时请求走本地网络，不会卡在已经死掉的隧道里。
    unawaited(_releaseWholesale());
    if (current()) {
      state = VpnStatus.disconnected;
      ref.read(connectionNoticeProvider.notifier).state = null;
    }
  }

  HandshakeReporter? _reporter(VpnEngine engine) =>
      engine is HandshakeReporter ? engine as HandshakeReporter : null;

  bool _waitsForHandshake(VpnEngine engine) =>
      engine.canRecover || (_reporter(engine)?.reportsHandshake ?? false);

  Future<ConnectProfile> _reopenLine({
    required VpnEngine engine,
    required Session? session,
    required NodeItem line,
    required String protocol,
    required bool demo,
    required bool Function() current,
    required bool Function() validAttempt,
  }) async {
    if (!current()) throw const _AttemptAborted();
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final confirmed = _lastLines
        .where((n) =>
            n.id == line.id && n.available && n.protocols.contains(protocol))
        .firstOrNull;
    if (confirmed == null || confirmed.probeHost == null) {
      throw const FormatException('线路状态已变化');
    }
    if (!demo && confirmed.id.startsWith('w')) {
      final profile = await ref
          .read(connectionApiProvider)
          .worldvpnProfile(session!, confirmed.id);
      if (!current()) throw const _AttemptAborted();
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      _wholesaleLine = confirmed.id;
      _wholesaleUnknown = false;
      return profile;
    }
    if (demo) {
      return ConnectProfile(
          nodeId: confirmed.id, protocol: 'MOCK', server: confirmed.probeHost!);
    }
    await _leaveWholesale();
    if (!current()) throw const _AttemptAborted();
    final native = engine as WireGuardVpnEngine;
    final key = await native.publicKey(vpnIdentity(session!));
    if (!current()) throw const _AttemptAborted();
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final api = ref.read(connectionApiProvider);
    await api.uploadPublicKey(session, key);
    if (!current()) throw const _AttemptAborted();
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    final profile = await api.profile(session, confirmed.id, key);
    if (!current()) throw const _AttemptAborted();
    if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
    return profile;
  }

  /// 配置收下之后还要等握手。超时就换同一地区的下一条，最多再试两条。
  Future<void> _commitConnect({
    required VpnEngine engine,
    required ConnectProfile profile,
    required NodeItem line,
    required bool Function() current,
    required bool Function() validAttempt,
    required Future<ConnectProfile> Function(NodeItem next) reopen,
  }) async {
    var currentProfile = profile;
    var currentLine = line;
    for (var hop = 0; hop < 3; hop++) {
      // 只有明确会上报握手结果的引擎才在这里等。安卓的自动重连仍走原来的状态流，
      // 避免第一次失败时少拦住流量。
      final reporter = _reporter(engine);
      if (reporter == null || !reporter.reportsHandshake) {
        _acceptEngineEvents = true;
        await engine.connect(currentProfile);
        _remember(currentLine.id);
        return;
      }
      final watch = _HandshakeWatch(engine);
      _connectInFlight = true;
      try {
        _acceptEngineEvents = false;
        await engine.connect(currentProfile);
        if (!current()) throw const _AttemptAborted();
        final failure = await watch.untilSettled(
          timeout: const Duration(seconds: 32),
          isCurrent: current,
        );
        if (!current()) throw const _AttemptAborted();
        if (failure != null) throw failure;
        _acceptEngineEvents = true;
        _failuresOnLine = 0;
        _badStreak = 0;
        _triedLines.clear();
        state = VpnStatus.connected;
        if (!_userStop && engine.canRecover) _arm(engine);
        _remember(currentLine.id);
        if (ref.read(selectedNodeProvider) != currentLine.id) {
          _suppressSelect = true;
          ref.read(selectedNodeProvider.notifier).state = currentLine.id;
          _suppressSelect = false;
        }
        ref.read(connectionFailureProvider.notifier).state = null;
        ref.read(connectionNoticeProvider.notifier).state =
            hop == 0 ? null : '已改连 ${currentLine.name}';
        return;
      } on _AttemptAborted {
        rethrow;
      } catch (error) {
        if (!current() || error is _AttemptAborted) rethrow;
        final diagnostic = reporter.tunnelDiagnostic;
        final next = error is VpnFailure &&
                error.code == 'HANDSHAKE_TIMEOUT' &&
                hop < 2
            ? nextLineInArea(currentLine, _lastLines, {..._triedLines, currentLine.id})
            : null;
        if (next == null) rethrow;
        unawaited(_reportConnectFailure(error, diagnostic: diagnostic));
        _triedLines.add(currentLine.id);
        try {
          await engine.disconnect();
        } catch (_) {}
        if (!current()) throw const _AttemptAborted();
        await _releaseWholesale();
        if (!current()) throw const _AttemptAborted();
        state = VpnStatus.connecting;
        ref.read(connectionFailureProvider.notifier).state = null;
        ref.read(connectionNoticeProvider.notifier).state =
            '这条线路握手超时，正在换同一地区的${next.name}';
        currentProfile = await reopen(next);
        currentLine = next;
      } finally {
        _connectInFlight = false;
        await watch.close();
      }
    }
  }

  String _stageFor(VpnFailure failure) {
    return switch (failure.code) {
      'HANDSHAKE_TIMEOUT' => 'handshake',
      'TUNNEL_SERVICE_STOPPED' => 'service',
      'PERMISSION_DENIED' || 'XRAY_MISSING' || 'WIREGUARD_MISSING' => 'prepare',
      'CONNECT_FAILED' => 'install',
      _ => 'connect',
    };
  }

  Future<void> _reportConnectFailure(Object error, {TunnelDiagnostic? diagnostic}) async {
    if (error is _AttemptAborted) return;
    final session = ref.read(sessionProvider).valueOrNull;
    if (session == null) return;
    final engine = ref.read(vpnEngineProvider);
    final fromEngine = diagnostic ?? _reporter(engine)?.tunnelDiagnostic;
    String stage = 'connect';
    String code = '';
    String text = '';
    if (error is VpnFailure) {
      stage = fromEngine?.stage.isNotEmpty == true ? fromEngine!.stage : _stageFor(error);
      code = fromEngine != null && fromEngine.errorCode.isNotEmpty
          ? fromEngine.errorCode
          : error.code;
      text = fromEngine != null && fromEngine.errorText.isNotEmpty
          ? fromEngine.errorText
          : error.message;
    } else if (error is ApiException) {
      code = 'HTTP_${error.code}';
      text = error.message;
    } else if (error is FormatException) {
      code = 'NODE_UNAVAILABLE';
      text = error.message;
    } else {
      return;
    }
    try {
      await ref.read(connectionApiProvider).reportTunnelDiagnostic(
            session,
            stage: stage,
            errorCode: code,
            errorText: redactTunnelText(text),
            serviceState: redactTunnelText(fromEngine?.serviceState ?? ''),
            logSummary: redactTunnelText(fromEngine?.logSummary ?? ''),
          );
    } catch (_) {}
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
      _userEpoch++;
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
            userInitiated: userInitiated,
            current: current,
            validAttempt: validAttempt);
        return;
      }
      final protocol = demo ? 'MOCK' : 'WIREGUARD';
      final nodes = await ref.refresh(nodesProvider.future);
      if (!current()) return;
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      _rememberLines(nodes);
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
          if (!validAttempt()) {
            throw const FormatException('连接选择或账户已变化');
          }
          if (node == null) {
            // WireGuard 走 UDP。探测端口全部拒绝时，仍按服务端顺序试连，最多 3 条。
            await _connectProbeFallback(
              probeable,
              engine: engine,
              session: demo ? null : session,
              protocol: protocol,
              demo: demo,
              userInitiated: userInitiated,
              current: current,
              validAttempt: validAttempt,
            );
            return;
          }
        } else if (!demo &&
            egressPlatformSupported &&
            engine is WireGuardVpnEngine) {
          final egressNode = preferredEgress(nodes);
          if (egressNode == null || !validAttempt()) {
            throw await _noLineOrPlan();
          }
          await _connectEgress(engine, egressNode.id, session!,
              userInitiated: userInitiated,
              current: current,
              validAttempt: validAttempt);
          return;
        } else {
          throw await _noLineOrPlan();
        }
      }
      if (!await engine.prepare()) {
        if (current()) {
          // 自动重连途中流量可能正拦着：不能显示成「未连接」，否则界面上没有恢复网络的按钮。
          state = userInitiated ? VpnStatus.disconnected : VpnStatus.error;
          ref.read(connectionFailureProvider.notifier).state =
              const VpnFailure('PERMISSION_DENIED', '未获得连接权限');
          unawaited(_reportConnectFailure(
              const VpnFailure('PERMISSION_DENIED', '未获得连接权限')));
        }
        return;
      }
      if (!current()) return;
      if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
      final fresh = await ref.refresh(nodesProvider.future);
      if (!current()) return;
      _rememberLines(fresh);
      final confirmed = fresh
          .where((n) =>
              n.id == node!.id && n.available && n.protocols.contains(protocol))
          .firstOrNull;
      if (confirmed == null || confirmed.probeHost == null || !validAttempt()) {
        throw const FormatException('线路状态已变化');
      }
      if (!demo && confirmed.id.startsWith('w')) {
        if (!worldvpnPlatformSupported) {
          throw const VpnFailure('ENGINE_UNAVAILABLE', '当前设备暂不支持该线路类型');
        }
        // 服务端为这台设备先删旧 peer、再建新 peer：重连或换另一条批发线路都不会叠加。
        final profile = await ref
            .read(connectionApiProvider)
            .worldvpnProfile(session!, confirmed.id);
        if (!current()) return;
        if (!validAttempt()) throw const FormatException('连接选择或账户已变化');
        _wholesaleLine = confirmed.id;
        _wholesaleUnknown = false;
        await _commitConnect(
          engine: engine,
          profile: profile,
          line: confirmed,
          current: current,
          validAttempt: validAttempt,
          reopen: (next) => _reopenLine(
            engine: engine,
            session: session,
            line: next,
            protocol: protocol,
            demo: demo,
            current: current,
            validAttempt: validAttempt,
          ),
        );
        return;
      }
      ConnectProfile profile;
      if (demo) {
        profile = ConnectProfile(
            nodeId: confirmed.id,
            protocol: 'MOCK',
            server: confirmed.probeHost!);
      } else {
        await _leaveWholesale();
        if (!current()) return;
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
      await _commitConnect(
        engine: engine,
        profile: profile,
        line: confirmed,
        current: current,
        validAttempt: validAttempt,
        reopen: (next) => _reopenLine(
          engine: engine,
          session: session,
          line: next,
          protocol: protocol,
          demo: demo,
          current: current,
          validAttempt: validAttempt,
        ),
      );
    } catch (error) {
      if (!current() || error is _AttemptAborted) return;
      unawaited(_reportConnectFailure(error));
      _acceptEngineEvents = false;
      // 用户这次连接没成：还掉刚建的批发 peer。自动重连途中不还——下一次重连时服务端会替
      // 这台设备先删旧 peer；在这里异步释放反而可能和下一次连接撞上，删掉刚建好的那条。
      // 重连彻底放弃时（致命失败）由 _recover 统一释放。
      if (userInitiated || !_armed) unawaited(_releaseWholesale());
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
            40902 => VpnFailure('DEVICE_LIMIT',
                e.message.isEmpty ? '同时在线的设备已达上限，请先在其他设备断开后再试' : e.message),
            50304 =>
              const VpnFailure('ADDRESS_POOL_EXHAUSTED', '线路资源暂时不足，请稍后重试'),
            _ => VpnFailure(
                'REQUEST_FAILED', e.message.isEmpty ? '连接失败，请重试' : e.message),
          },
        FormatException e => VpnFailure('NODE_UNAVAILABLE',
            e.message.isEmpty ? '线路暂不可用，请刷新后重新选择' : e.message),
        DioException e when isConnectTimeout(e) =>
          const VpnFailure('CONNECT_TIMEOUT', '连接超时，请重试'),
        _ => const VpnFailure('CONNECT_FAILED', '连接失败，请重试'),
      };
    }
  }
}

final connectionControllerProvider =
    NotifierProvider<ConnectionController, VpnStatus>(ConnectionController.new);

/// 用户在这次尝试结束前改了主意（断开、换线、退出）。调用方应安静停下，不再换下一条。
class _AttemptAborted implements Exception {
  const _AttemptAborted();
}

/// 记下连接过程中引擎报出的失败或「已连接」，供探测失败后的换线使用。
class _HandshakeWatch {
  _HandshakeWatch(VpnEngine engine) {
    _statusSub = engine.status.listen((status) {
      if (status == VpnStatus.connected) _connected = true;
    });
    _failSub = engine.failures.listen((failure) {
      _failure ??= failure;
    });
  }

  StreamSubscription<VpnStatus>? _statusSub;
  StreamSubscription<VpnFailure>? _failSub;
  VpnFailure? _failure;
  var _connected = false;

  VpnFailure? get failure => _failure;

  Future<VpnFailure?> untilSettled({
    required Duration timeout,
    required bool Function() isCurrent,
  }) async {
    final deadline = DateTime.now().add(timeout);
    while (true) {
      if (!isCurrent()) throw const _AttemptAborted();
      if (_failure != null) return _failure;
      if (_connected) return null;
      if (!DateTime.now().isBefore(deadline)) {
        return const VpnFailure('HANDSHAKE_TIMEOUT', '线路握手超时，请重试或更换线路');
      }
      await Future<void>.delayed(const Duration(milliseconds: 50));
    }
  }

  Future<void> close() async {
    await _statusSub?.cancel();
    await _failSub?.cancel();
    _statusSub = null;
    _failSub = null;
  }
}
