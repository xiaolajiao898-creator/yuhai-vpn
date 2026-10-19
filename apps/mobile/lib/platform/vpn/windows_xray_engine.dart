import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'egress_profile.dart';
import 'vpn_engine.dart';
import 'windows_job.dart';
import 'windows_wireguard_engine.dart';
import 'windows_xray_config.dart';
import 'windows_xray_process.dart';
import 'wireguard_profile.dart';

/// 驱动连接后的探测与统计。测试用手动触发，避免依赖真实计时器。
abstract interface class SampleTicker {
  void start(Duration interval, Future<void> Function() tick);
  void stop();
}

class PeriodicSampleTicker implements SampleTicker {
  Timer? _timer;

  @override
  void start(Duration interval, Future<void> Function() tick) {
    stop();
    _timer = Timer.periodic(interval, (_) {
      unawaited(tick());
    });
  }

  @override
  void stop() {
    _timer?.cancel();
    _timer = null;
  }
}

enum _TunnelMode { idle, xray, wireGuard }

/// Windows 出口引擎：校验服务端 Xray JSON，补上 TUN 字段后从标准输入交给 xray.exe。
///
/// 配置不写磁盘。xray 被放进 Job Object，应用退出或崩溃时一起结束。只有从隧道地址
/// 发出的 HTTPS 探测成功才报告已连接，之后每分钟复核一次。流量读 Wintun 网卡计数，
/// 不在本机开 API 端口。
///
/// WireGuard 引擎仍可在确实存在 WireGuard 节点时被调用；没有这类节点时连接控制器
/// 不会走到它，界面也不会提示去安装 WireGuard。
class WindowsXrayEngine implements WireGuardVpnEngine {
  WindowsXrayEngine({
    XrayLauncher? launch,
    Future<bool> Function()? probe,
    Future<({int rx, int tx})?> Function(String interfaceName)? readStats,
    ProcessSupervisor? supervisor,
    Future<bool> Function()? isAdmin,
    Future<bool> Function()? binariesReady,
    String? binaryDirectory,
    SampleTicker? ticker,
    WindowsWireGuardEngine? wireGuard,
    bool allowWireGuard = false,
    DateTime Function()? now,
    Duration pollInterval = const Duration(seconds: 1),
    Duration handshakeTimeout = const Duration(seconds: 25),
    int recheckEvery = 60,
  })  : _launch = launch ?? launchXrayProcess,
        _probe = probe ?? probeTunnelHttps,
        _readStats = readStats ?? readWindowsAdapterStats,
        _supervisor = supervisor ?? defaultProcessSupervisor(),
        _isAdmin = isAdmin ?? windowsIsAdministrator,
        _binariesReady = binariesReady ??
            (() => windowsXrayBinariesReady(
                binaryDirectory ?? defaultXrayDirectory())),
        _binaryDirectory = binaryDirectory ?? defaultXrayDirectory(),
        _ticker = ticker ?? PeriodicSampleTicker(),
        _wireGuard = wireGuard,
        _allowWireGuard = allowWireGuard && wireGuard != null,
        _now = now ?? DateTime.now,
        _pollInterval = pollInterval,
        _handshakeTimeout = handshakeTimeout,
        _recheckEvery = recheckEvery {
    final wireGuardEngine = _wireGuard;
    if (wireGuardEngine != null) {
      _wireGuardStatus = wireGuardEngine.status.listen((status) {
        if (_mode == _TunnelMode.wireGuard) _set(status);
      });
      _wireGuardStats = wireGuardEngine.stats.listen((stats) {
        if (_mode == _TunnelMode.wireGuard && !_disposed) _stats.add(stats);
      });
      _wireGuardFailures = wireGuardEngine.failures.listen((failure) {
        if (_mode == _TunnelMode.wireGuard) _emitFailure(failure);
      });
    }
  }

  static const _arguments = ['run', '-c', 'stdin:'];

  final XrayLauncher _launch;
  final Future<bool> Function() _probe;
  final Future<({int rx, int tx})?> Function(String interfaceName) _readStats;
  final ProcessSupervisor _supervisor;
  final Future<bool> Function() _isAdmin;
  final Future<bool> Function() _binariesReady;
  final String _binaryDirectory;
  final SampleTicker _ticker;
  final WindowsWireGuardEngine? _wireGuard;
  final bool _allowWireGuard;
  final DateTime Function() _now;
  final Duration _pollInterval;
  final Duration _handshakeTimeout;
  final int _recheckEvery;

  // 同步投递：探测结果返回时，等待方已经能读到新状态。
  final _status = StreamController<VpnStatus>.broadcast(sync: true);
  final _stats = StreamController<VpnStats>.broadcast(sync: true);
  final _failures = StreamController<VpnFailure>.broadcast(sync: true);
  StreamSubscription<VpnStatus>? _wireGuardStatus;
  StreamSubscription<VpnStats>? _wireGuardStats;
  StreamSubscription<VpnFailure>? _wireGuardFailures;

  int _generation = 0;
  bool _disposed = false;
  bool _expectExit = false;
  bool _sampling = false;
  _TunnelMode _mode = _TunnelMode.idle;
  SpawnedXray? _process;
  DateTime? _phaseStarted;
  DateTime? _connectedAt;
  String? _interfaceName;
  int _samples = 0;
  int _lastRx = 0;
  int _lastTx = 0;
  DateTime? _lastSample;

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
    _status.add(status);
  }

  void _emitFailure(VpnFailure failure) {
    if (_disposed) return;
    _failures.add(failure);
    _set(VpnStatus.error);
  }

  @override
  Future<String> publicKey(String identity) {
    final wireGuard = _wireGuard;
    if (wireGuard == null) {
      throw const FormatException('VPN 身份无效');
    }
    return wireGuard.publicKey(identity);
  }

  @override
  Future<bool> prepare() async {
    if (_disposed) return false;
    if (!await _binariesReady()) {
      throw const VpnFailure('XRAY_MISSING', '安装不完整：缺少 Xray 组件，请重新安装');
    }
    if (!await _isAdmin()) {
      _emitFailure(const VpnFailure('PERMISSION_DENIED', '请右键宇海VPN图标，选择「以管理员身份运行」'));
      return false;
    }
    return true;
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    if (profile is WireGuardProfile && profile.protocol == 'WIREGUARD') {
      if (!_allowWireGuard) {
        throw const FormatException('当前版本的 Windows 客户端请选择出口线路');
      }
      await _stopXray();
      _mode = _TunnelMode.wireGuard;
      await _wireGuard!.connect(profile);
      return;
    }
    if (profile is! EgressProfile) {
      throw const FormatException('只接受已验证的出口配置');
    }
    if (!profile.expiresAt.isAfter(_now())) {
      throw const VpnFailure('PROFILE_EXPIRED', '连接资料已过期，请重新连接');
    }
    final String runtime;
    try {
      runtime = windowsRuntimeConfig(profile.config);
    } on FormatException {
      throw const VpnFailure('PROFILE_INVALID', '线路连接资料无效，请换一条线路或联系客服');
    }
    final interfaceName = windowsRuntimeInterfaceName(runtime);
    final generation = ++_generation;
    await _stopXray();
    if (_mode == _TunnelMode.wireGuard) {
      _mode = _TunnelMode.idle;
      await _wireGuard?.disconnect().catchError((Object _) {});
    }
    if (generation != _generation || _disposed) return;
    _mode = _TunnelMode.xray;
    _phaseStarted = _now();
    _connectedAt = null;
    _interfaceName = interfaceName;
    _samples = 0;
    _lastSample = null;
    _lastRx = 0;
    _lastTx = 0;
    _set(VpnStatus.connecting);
    SpawnedXray spawned;
    try {
      spawned = await _launch(
        executable: '$_binaryDirectory\\xray.exe',
        arguments: _arguments,
        workingDirectory: _binaryDirectory,
      );
      if (generation != _generation || _disposed) {
        _abandon(spawned);
        return;
      }
      // 先纳入作业再写入配置：纳入失败时凭据不会进入子进程。
      try {
        await _supervisor.adopt(spawned.pid);
      } catch (_) {
        _abandon(spawned);
        throw const VpnFailure('CONNECT_FAILED', 'VPN 连接失败，请重试');
      }
      if (generation != _generation || _disposed) {
        _abandon(spawned);
        return;
      }
      spawned.stdin.add(utf8.encode(runtime));
      await spawned.stdin.close();
    } on VpnFailure {
      rethrow;
    } catch (_) {
      throw const VpnFailure('CONNECT_FAILED', 'VPN 连接失败，请重试');
    }
    if (generation != _generation || _disposed) {
      _abandon(spawned);
      return;
    }
    _process = spawned;
    _expectExit = false;
    unawaited(spawned.exitCode.then((_) {
      if (_disposed || generation != _generation || _expectExit) return;
      _expectExit = true;
      _ticker.stop();
      _process = null;
      _emitFailure(const VpnFailure('NETWORK_UNAVAILABLE', '网络暂不可用，请检查网络后重试'));
    }));
    await _sample(generation);
    if (generation == _generation && !_disposed && _process == spawned) {
      _ticker.start(_pollInterval, () => _sample(generation));
    }
  }

  Future<void> _sample(int generation) async {
    if (_disposed || generation != _generation || _sampling) return;
    _sampling = true;
    try {
      if (_disposed || generation != _generation) return;
      final started = _phaseStarted ?? _now();
      if (_connectedAt == null) {
        final ok = await _probe().catchError((Object _) => false);
        if (_disposed || generation != _generation) return;
        if (ok) {
          _connectedAt = _now();
          _samples = 0;
          _lastSample = null;
          _set(VpnStatus.connected);
          return;
        }
        if (_now().difference(started) >= _handshakeTimeout) {
          await _failHandshake(generation);
        }
        return;
      }
      _samples++;
      if (_recheckEvery > 0 && _samples % _recheckEvery == 0) {
        final ok = await _probe().catchError((Object _) => false);
        if (_disposed || generation != _generation) return;
        if (!ok) {
          _connectedAt = null;
          _phaseStarted = _now();
          _samples = 0;
          _set(VpnStatus.reconnecting);
          return;
        }
      }
      final name = _interfaceName;
      if (name == null) return;
      final reading = await _readStats(name);
      if (_disposed || generation != _generation || reading == null) return;
      if (_connectedAt == null) return;
      final now = _now();
      final previous = _lastSample;
      final up = previous == null
          ? 0
          : _speed(reading.tx - _lastTx, now.difference(previous));
      final down = previous == null
          ? 0
          : _speed(reading.rx - _lastRx, now.difference(previous));
      _lastTx = reading.tx;
      _lastRx = reading.rx;
      _lastSample = now;
      _stats.add(VpnStats(
        upBytes: reading.tx,
        downBytes: reading.rx,
        upSpeed: up,
        downSpeed: down,
        since: _connectedAt!.toUtc(),
      ));
    } finally {
      _sampling = false;
    }
  }

  int _speed(int deltaBytes, Duration elapsed) {
    final millis = elapsed.inMilliseconds;
    if (millis <= 0 || deltaBytes <= 0) return 0;
    return (deltaBytes * 1000 / millis).round();
  }

  Future<void> _failHandshake(int generation) async {
    if (generation != _generation) return;
    _expectExit = true;
    _ticker.stop();
    _process?.kill();
    _process = null;
    _emitFailure(const VpnFailure('HANDSHAKE_TIMEOUT', '线路握手超时，请重试或更换线路'));
  }

  void _abandon(SpawnedXray spawned) {
    spawned.kill();
    unawaited(spawned.exitCode.then((_) {}));
  }

  Future<void> _stopXray() async {
    _expectExit = true;
    _ticker.stop();
    final process = _process;
    _process = null;
    process?.kill();
  }

  @override
  Future<void> disconnect() async {
    if (_mode == _TunnelMode.wireGuard) {
      _mode = _TunnelMode.idle;
      await _wireGuard?.disconnect();
      return;
    }
    ++_generation;
    _set(VpnStatus.disconnecting);
    await _stopXray();
    _connectedAt = null;
    _mode = _TunnelMode.idle;
    _set(VpnStatus.disconnected);
  }

  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) async {
    if (_disposed || !validVpnHost(host) || port < 1 || port > 65535) {
      return null;
    }
    final watch = Stopwatch()..start();
    try {
      final socket = await Socket.connect(host, port, timeout: timeout);
      watch.stop();
      socket.destroy();
      return watch.elapsedMilliseconds;
    } catch (_) {
      return null;
    }
  }

  void dispose() {
    if (_disposed) return;
    _disposed = true;
    _generation++;
    _expectExit = true;
    _ticker.stop();
    _process?.kill();
    _process = null;
    unawaited(_supervisor.release());
    unawaited(_wireGuardStatus?.cancel() ?? Future<void>.value());
    unawaited(_wireGuardStats?.cancel() ?? Future<void>.value());
    unawaited(_wireGuardFailures?.cancel() ?? Future<void>.value());
    _wireGuard?.dispose();
    unawaited(_status.close());
    unawaited(_stats.close());
    unawaited(_failures.close());
  }
}
