import 'dart:async';
import 'dart:io' show Platform;

import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

import 'egress_profile.dart';
import 'vpn_engine.dart';
import 'wireguard_profile.dart';

/// Method channel `yuhai/vpn`, event channel `yuhai/vpn/events`.
/// publicKey({identity}) -> base64 public key; prepare() -> bool;
/// connect(WireGuardProfile.nativeArguments(generation)) -> null;
/// disconnect({generation}) -> null, only after the tunnel is down;
/// probe({host, port, timeoutMs}) -> milliseconds or null.
/// connectXray(EgressProfile.nativeArguments(generation)) -> null（Android：Xray 内核出口；
/// 与 WireGuard 共用代次，原生侧先停掉另一种隧道再建立；「connected」只在经出口的真实
/// HTTPS 请求成功后上报，handshakeAt 为该请求成功时间）。
///
/// Every tunnel event includes identity and generation. Status events use
/// {type: 'status', status: 'connecting'|'connected'|'reconnecting'|
/// 'disconnecting'|'disconnected', handshakeConfirmed: true and handshakeAt:
/// UTC ISO8601 from a current-session handshake for connected}.
/// Stats events use {type: 'stats', upBytes, downBytes, upSpeed, downSpeed,
/// since: epochMilliseconds}. Error events use {type: 'error', code}.
/// Native code must serialize connection mutations, reject older generations,
/// isolate keys by identity, and never return/log its private key.
class NativeVpnEngine implements WireGuardVpnEngine {
  NativeVpnEngine({
    MethodChannel methodChannel = const MethodChannel('yuhai/vpn'),
    EventChannel eventChannel = const EventChannel('yuhai/vpn/events'),
    Stream<dynamic>? events,
    DateTime Function()? now,
    int Function()? nextGeneration,
  })  : _methods = methodChannel,
        _now = now ?? DateTime.now,
        _nextGeneration = nextGeneration ?? _allocateGeneration {
    _events = (events ?? eventChannel.receiveBroadcastStream()).listen(
      _event,
      onError: (Object _) {
        if (!_disposed && _identity != null) {
          _failure('ENGINE_UNAVAILABLE');
        }
      },
    );
  }

  static int _sequence = 0;
  static int _allocateGeneration() => ++_sequence;
  final int Function() _nextGeneration;
  final MethodChannel _methods;
  final DateTime Function() _now;
  final _status = StreamController<VpnStatus>.broadcast();
  final _stats = StreamController<VpnStats>.broadcast();
  final _failures = StreamController<VpnFailure>.broadcast();
  late final StreamSubscription<dynamic> _events;
  int _generation = 0;
  bool _disposed = false;
  String? _identity;
  DateTime? _startedAt;
  VpnStatus _current = VpnStatus.disconnected;
  Timer? _handshakeTimeout;

  @override
  bool get canProbe => !_disposed;
  @override
  Stream<VpnStatus> get status => _status.stream;
  @override
  Stream<VpnStats> get stats => _stats.stream;
  @override
  Stream<VpnFailure> get failures => _failures.stream;

  void _set(VpnStatus status) {
    if (_disposed) return;
    _current = status;
    _status.add(status);
  }

  static VpnFailure failureFor(String? code) => switch (code) {
        'PERMISSION_DENIED' =>
          const VpnFailure('PERMISSION_DENIED', '未获得 VPN 连接权限'),
        'HANDSHAKE_TIMEOUT' =>
          const VpnFailure('HANDSHAKE_TIMEOUT', '线路握手超时，请重试或更换线路'),
        'PROFILE_INVALID' =>
          const VpnFailure('PROFILE_INVALID', '线路连接资料无效，请换一条线路或联系客服'),
        'PROFILE_EXPIRED' =>
          const VpnFailure('PROFILE_EXPIRED', '连接资料已过期，请重新连接'),
        'NETWORK_UNAVAILABLE' =>
          const VpnFailure('NETWORK_UNAVAILABLE', '网络暂不可用，请检查网络后重试'),
        'DISCONNECT_FAILED' =>
          const VpnFailure('DISCONNECT_FAILED', '断开未获确认，请重试'),
        'ENGINE_UNAVAILABLE' =>
          const VpnFailure('ENGINE_UNAVAILABLE', '当前安装包未启用 VPN 引擎'),
        'ENGINE_NATIVE' => const VpnFailure('ENGINE_NATIVE',
            '当前设备无法加载 VPN 内核。电脑模拟器请安装带 x86_64 的测试包后再连接'),
        'VPN_REVOKED' =>
          const VpnFailure('VPN_REVOKED', 'VPN 已被系统或其他 VPN 应用接管，请重新连接'),
        _ => const VpnFailure('CONNECT_FAILED', 'VPN 连接失败，请重试'),
      };

  void _failure(String? code) {
    if (_disposed) return;
    _handshakeTimeout?.cancel();
    _failures.add(failureFor(code));
    _set(VpnStatus.error);
  }

  Future<T?> _invoke<T>(String method, [Map<String, Object>? arguments]) async {
    if (_disposed) throw failureFor('ENGINE_UNAVAILABLE');
    try {
      return await _methods.invokeMethod<T>(method, arguments);
    } on MissingPluginException {
      throw failureFor('ENGINE_UNAVAILABLE');
    } on PlatformException catch (e) {
      throw failureFor(e.code);
    }
  }

  @override
  Future<String> publicKey(String identity) async {
    if (!validVpnIdentity(identity)) throw const FormatException('VPN 身份无效');
    final result = await _invoke<String>('publicKey', {'identity': identity});
    if (!validWireGuardPublicKey(result)) {
      throw const FormatException('VPN 设备公钥无效');
    }
    return result!;
  }

  @override
  Future<bool> prepare() async => await _invoke<bool>('prepare') == true;

  @override
  Future<void> connect(ConnectProfile profile) async {
    final String method, identity;
    final DateTime expiresAt;
    final Map<String, Object> Function(int) arguments;
    if (profile is WireGuardProfile && profile.protocol == 'WIREGUARD') {
      method = 'connect';
      identity = profile.identity;
      expiresAt = profile.expiresAt;
      arguments = profile.nativeArguments;
    } else if (profile is EgressProfile) {
      method = 'connectXray';
      identity = profile.identity;
      expiresAt = profile.expiresAt;
      arguments = profile.nativeArguments;
    } else {
      throw const FormatException('原生引擎只接受已验证的连接配置');
    }
    if (!expiresAt.isAfter(_now())) throw failureFor('PROFILE_EXPIRED');
    final generation = _generation = _nextGeneration();
    _identity = identity;
    _startedAt = _now();
    _set(VpnStatus.connecting);
    _armHandshakeTimeout(generation);
    try {
      await _invoke<void>(method, arguments(generation));
      // Accepted configuration is not proof of a successful handshake.
    } catch (_) {
      if (!_disposed && generation == _generation) {
        _handshakeTimeout?.cancel();
        _set(VpnStatus.error);
      }
      rethrow;
    }
  }

  void _armHandshakeTimeout(int generation) {
    _handshakeTimeout?.cancel();
    _handshakeTimeout = Timer(const Duration(seconds: 30), () {
      if (_disposed ||
          generation != _generation ||
          (_current != VpnStatus.connecting &&
              _current != VpnStatus.reconnecting)) {
        return;
      }
      // Invalidate late native handshakes before requesting teardown.
      final stopped = disconnect();
      final stoppingGeneration = _generation;
      unawaited(stopped.then((_) {
        if (!_disposed && stoppingGeneration == _generation) {
          _failure('HANDSHAKE_TIMEOUT');
        }
      }).catchError((Object _) {
        if (!_disposed && stoppingGeneration == _generation) {
          _failure('DISCONNECT_FAILED');
        }
      }));
    });
  }

  @override
  Future<void> disconnect() async {
    final generation = _generation = _nextGeneration();
    _handshakeTimeout?.cancel();
    _set(VpnStatus.disconnecting);
    try {
      await _invoke<void>('disconnect', {'generation': generation});
    } catch (_) {
      if (!_disposed && generation == _generation) {
        _failure('DISCONNECT_FAILED');
      }
      rethrow;
    }
    if (!_disposed && generation == _generation) {
      _identity = null;
      _set(VpnStatus.disconnected);
    }
  }

  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) async {
    if (_disposed ||
        !validVpnHost(host) ||
        port < 1 ||
        port > 65535 ||
        timeout.inMilliseconds < 1 ||
        timeout.inSeconds > 10) {
      return null;
    }
    try {
      final result = await _invoke<int>('probe', {
        'host': host,
        'port': port,
        'timeoutMs': timeout.inMilliseconds,
      }).timeout(timeout + const Duration(milliseconds: 250));
      return result != null && result >= 0 && result <= timeout.inMilliseconds
          ? result
          : null;
    } catch (_) {
      return null;
    }
  }

  @override
  bool get canRecover => !kIsWeb && Platform.isAndroid;

  @override
  Future<void> applyPolicy({required bool leakProtect}) async {
    if (!canRecover) return;
    try {
      await _invoke<void>('setLeakProtect', {'enabled': leakProtect});
    } catch (_) {}
  }

  @override
  Future<int?> probeTunnel() async {
    if (!canRecover) return null;
    try {
      final result = await _invoke<int>('probeTunnel', {})
          .timeout(const Duration(seconds: 8));
      if (result == null || result < 0 || result > 15000) return null;
      return result;
    } catch (_) {
      return null;
    }
  }

  @override
  Future<bool> holdTraffic() async {
    if (!canRecover) return false;
    try {
      return await _invoke<bool>('holdTraffic', {}) == true;
    } catch (_) {
      return false;
    }
  }

  @override
  Future<void> releaseTraffic() async {
    if (!canRecover) return;
    try {
      await _invoke<void>('releaseTraffic', {});
    } catch (_) {}
  }

  @override
  Future<int> networkEpoch() async {
    if (!canRecover) return 0;
    try {
      return await _invoke<int>('networkEpoch', {}) ?? 0;
    } catch (_) {
      return 0;
    }
  }

  void _event(dynamic value) {
    if (_disposed ||
        value is! Map ||
        _identity == null ||
        value['identity'] != _identity ||
        value['generation'] != _generation) {
      return;
    }
    switch (value['type'] ?? (value.containsKey('status') ? 'status' : null)) {
      case 'status':
        if (value['status'] == 'error') {
          _failure(value['code'] is String ? value['code'] : null);
          return;
        }
        final status = switch (value['status']) {
          'connecting' => VpnStatus.connecting,
          'connected' when _confirmedHandshake(value) => VpnStatus.connected,
          'reconnecting' => VpnStatus.reconnecting,
          'disconnecting' => VpnStatus.disconnecting,
          'disconnected' => VpnStatus.disconnected,
          _ => null,
        };
        if (status == null || _current == VpnStatus.error) return;
        if (status == VpnStatus.connected || status == VpnStatus.disconnected) {
          _handshakeTimeout?.cancel();
        }
        if (status == VpnStatus.disconnected) _identity = null;
        if (status == VpnStatus.reconnecting &&
            _current != VpnStatus.reconnecting) {
          _startedAt = _now();
          _armHandshakeTimeout(_generation);
        }
        _set(status);
      case 'error':
        _failure(value['code'] is String ? value['code'] : null);
      case 'stats':
        if (_current != VpnStatus.connected) return;
        const fields = ['upBytes', 'downBytes', 'upSpeed', 'downSpeed'];
        if (fields.any((key) => value[key] is! int || value[key] < 0)) return;
        final rawSince = value['since'];
        final since =
            rawSince is int && rawSince > 0 && rawSince < 8640000000000000
                ? DateTime.fromMillisecondsSinceEpoch(rawSince, isUtc: true)
                : null;
        if (since == null ||
            since.isAfter(_now().add(const Duration(seconds: 5)))) {
          return;
        }
        _stats.add(VpnStats(
            upBytes: value['upBytes'],
            downBytes: value['downBytes'],
            upSpeed: value['upSpeed'],
            downSpeed: value['downSpeed'],
            since: since));
    }
  }

  bool _confirmedHandshake(Map value) {
    final raw = value['handshakeAt'];
    final handshake = raw is String ? DateTime.tryParse(raw) : null;
    return value['handshakeConfirmed'] == true &&
        handshake != null &&
        _startedAt != null &&
        !handshake.isBefore(_startedAt!.subtract(const Duration(seconds: 1))) &&
        !handshake.isAfter(_now().add(const Duration(seconds: 5)));
  }

  void dispose() {
    if (_disposed) return;
    _disposed = true;
    _generation = _nextGeneration();
    _handshakeTimeout?.cancel();
    unawaited(_events.cancel());
    unawaited(_methods.invokeMethod<void>(
        'disconnect', {'generation': _generation}).catchError((Object _) {}));
    unawaited(_status.close());
    unawaited(_stats.close());
    unawaited(_failures.close());
  }
}
