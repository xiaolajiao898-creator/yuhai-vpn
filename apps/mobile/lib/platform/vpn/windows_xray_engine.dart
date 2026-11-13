import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'egress_profile.dart';
import 'network_epoch.dart';
import 'tunnel_host_pins.dart';
import 'tunnel_diagnostic.dart';
import 'vpn_engine.dart';
import 'windows_job.dart';
import 'windows_traffic_guard.dart';
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
/// 发出的 HTTPS 探测成功才报告已连接，之后大约每 20 秒复核一次。流量读 Wintun 网卡计数，
/// 不在本机开 API 端口。断网保护用动态 WFP 会话，进程退出后规则消失。
///
/// WireGuard 引擎仍可在确实存在 WireGuard 节点时被调用；没有这类节点时连接控制器
/// 不会走到它，界面也不会提示去安装 WireGuard。
class WindowsXrayEngine implements WireGuardVpnEngine, HandshakeReporter {
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
    int recheckEvery = 20,
    WindowsTrafficGuard? trafficGuard,
    TunnelHostMemory? tunnelHosts,
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
        _recheckEvery = recheckEvery,
        _guard = trafficGuard ?? WindowsTrafficGuard(),
        _tunnelHosts = tunnelHosts {
    final wireGuardEngine = _wireGuard;
    if (wireGuardEngine != null) {
      // 握手超时会卸掉隧道服务（连同它自带的防火墙）。断网保护开着时先装好自己的规则。
      wireGuardEngine.beforeTimeoutTeardown = () async {
        if (_mode == _TunnelMode.wireGuard && _leakProtect) await _engageGuard();
      };
      _wireGuardStatus = wireGuardEngine.status.listen((status) {
        if (_mode != _TunnelMode.wireGuard) return;
        // 新隧道已有握手：WireGuard 服务自带的防火墙从服务启动起就在拦隧道外的流量，
        // 这时再撤掉重连期间的规则，中间没有空档。
        if (status == VpnStatus.connected) _releaseRecoveryGuard();
        _set(status);
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
  final WindowsTrafficGuard _guard;

  /// 记下隧道服务器域名的地址，断网保护拦着、系统查不到域名时用（见 tunnel_host_pins.dart）。
  /// 为 null 时不做这件事，连接资料原样使用。
  final TunnelHostMemory? _tunnelHosts;

  /// 线路编号 → 线路列表里给的探测地址，由 [noteLineHosts] 交来。
  Map<String, String> _lineHosts = const {};
  Future<void>? _lineHostsLookup;

  /// 一次最多预先查这么多个线路域名，免得列表很长时一直在后台查。
  static const _maxLineLookups = 32;
  final NetworkEpoch _epoch = NetworkEpoch();
  bool _leakProtect = true;

  /// 重连期间装上的拦截规则还在。新隧道确认可用后撤掉，用户断开时也撤掉。
  bool _guarding = false;

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
  bool get canRecover => !_disposed && Platform.isWindows;

  @override
  bool get reportsHandshake => !_disposed;

  TunnelDiagnostic? _ownDiagnostic;

  @override
  TunnelDiagnostic? get tunnelDiagnostic => _ownDiagnostic ?? _wireGuard?.tunnelDiagnostic;

  @override
  Future<void> applyPolicy({required bool leakProtect}) async {
    _leakProtect = leakProtect;
  }

  @override
  Future<int?> probeTunnel() async {
    if (_disposed) return null;
    if (_mode == _TunnelMode.wireGuard) {
      // WireGuard（自建节点、WorldVPN 批发线路）看握手：有 25 秒保活时大约每两分钟重新握手，
      // 超过 200 秒没有新握手才算断（Reject-After-Time 是 180 秒）。返回值不是延迟，只表示会话有效。
      final age = await _wireGuard?.handshakeAgeSeconds();
      if (_disposed || _mode != _TunnelMode.wireGuard) return null;
      if (age == null || age > 200) return null;
      return 1;
    }
    if (_mode != _TunnelMode.xray || _process == null) return null;
    final watch = Stopwatch()..start();
    final ok = await _probe().catchError((Object _) => false);
    watch.stop();
    if (!ok) return null;
    final ms = watch.elapsedMilliseconds;
    if (ms < 0 || ms > 15000) return null;
    return ms;
  }

  /// 只放行本程序、xray.exe 和 wireguard.exe（存在的才放进名单：
  /// 不存在的文件取不到 AppId，整组规则会装不上）。
  Future<bool> _engageGuard() async {
    if (_disposed || !_leakProtect || !Platform.isWindows) return false;
    final wireGuard = _wireGuard;
    final permits = <String>[
      Platform.resolvedExecutable,
      '$_binaryDirectory\\xray.exe',
      if (wireGuard != null) wireGuard.wireguardExePath,
    ].where((path) => File(path).existsSync()).toList();
    final held = await _guard.hold(permits);
    if (held) _guarding = true;
    return held;
  }

  void _releaseRecoveryGuard() {
    if (!_guarding) return;
    _guarding = false;
    _guard.release();
  }

  /// 没拦着时：顺手记下 Xray 要连的服务器域名的地址，配置不变。
  /// 拦着、系统又查不到这些域名时：把记下的地址写进这次的配置（`dns.hosts`），
  /// 让 xray.exe 不经系统 DNS 也能连上；地址和 SNI 不变。改完的配置校验不过就用原样的。
  Future<String> _pinXrayHosts(String runtime) async {
    final memory = _tunnelHosts;
    if (memory == null) return runtime;
    final List<String> hosts;
    try {
      hosts = xrayProxyHosts(runtime);
    } on FormatException {
      return runtime;
    }
    if (hosts.isEmpty) return runtime;
    if (!_guarding) {
      unawaited(memory.remember(hosts));
      return runtime;
    }
    final pins = await memory.pinsFor(hosts);
    if (pins.isEmpty) return runtime;
    try {
      final pinned = pinXrayHosts(runtime, pins);
      checkXrayServerConfig(pinned);
      return pinned;
    } on FormatException {
      return runtime;
    }
  }

  /// WireGuard 同理：拦着、查不到服务器域名时返回记下的 `IP:端口`，否则返回 null（照用资料里的）。
  ///
  /// 兜底：批发线路从没连过、域名也没记过时，用线路列表里这条线路的探测地址（批发线路给的是
  /// 服务器 IP）。WireGuard 按公钥认服务器，IP 不对只会握手失败，不会连到别人那里。
  /// 自建线路没有这个兜底：它的探测地址就是 endpoint 的主机，已经由 [noteLineHosts] 预先查过。
  Future<String?> _pinnedWireGuardEndpoint(WireGuardProfile profile) async {
    final endpoint = profile.endpoint;
    final memory = _tunnelHosts;
    final host = wireGuardEndpointHost(endpoint);
    if (memory == null || host == null) return null;
    if (!_guarding) {
      unawaited(memory.remember([host]));
      return null;
    }
    final addresses = (await memory.pinsFor([host]))[host];
    if (addresses != null && addresses.isNotEmpty) {
      return pinWireGuardEndpoint(endpoint, addresses.first);
    }
    if (profile.issuedPrivateKey == null ||
        InternetAddress.tryParse(host) != null) {
      return null;
    }
    final probe = _lineHosts['w${profile.nodeId}'];
    final ip = probe == null ? null : InternetAddress.tryParse(probe);
    return ip == null ? null : pinWireGuardEndpoint(endpoint, ip);
  }

  /// 连接控制器每次刷新线路列表后调用（线路编号 → 探测地址）：
  /// - 记下各线路的探测地址，供上面批发线路的兜底使用；
  /// - 没拦着时顺手查一遍其中的域名并记下：出口线路和自建线路的探测地址就是要连的服务器，
  ///   重连时换到一条以前没连过的线路，拦着的时候也有 IP 可用。查过程在后台，不耽误连接。
  void noteLineHosts(Map<String, String> hostByLine) {
    final memory = _tunnelHosts;
    if (memory == null || _disposed) return;
    _lineHosts = Map.unmodifiable(hostByLine);
    if (_guarding || _lineHostsLookup != null) return;
    final hosts = hostByLine.values
        .where((h) => InternetAddress.tryParse(h) == null)
        .toSet()
        .take(_maxLineLookups)
        .toList();
    if (hosts.isEmpty) return;
    _lineHostsLookup =
        memory.remember(hosts).whenComplete(() => _lineHostsLookup = null);
  }

  /// 拦住除本程序、xray.exe、wireguard.exe 以外的出站连接，然后停掉当前隧道：
  /// WireGuard 隧道服务自带的防火墙会挡住本程序直连服务端取新的连接材料，必须在
  /// 自己的规则装好之后卸掉；Xray 的 TUN 还在时本程序的请求会掉进那条已经不通的隧道。
  /// 规则一直留到新隧道确认可用，或用户点「断开」。
  @override
  Future<bool> holdTraffic() async {
    final held = await _engageGuard();
    if (!held) return false;
    if (_mode == _TunnelMode.wireGuard) {
      _mode = _TunnelMode.idle;
      try {
        await _wireGuard?.disconnect();
      } catch (_) {}
    } else if (_mode == _TunnelMode.xray) {
      await _stopXray();
      _connectedAt = null;
      _mode = _TunnelMode.idle;
    }
    return true;
  }

  @override
  Future<void> releaseTraffic() async {
    _guarding = false;
    _guard.release();
  }

  @override
  Future<int> networkEpoch() async {
    if (_disposed || !Platform.isWindows) return 0;
    try {
      final interfaces = await NetworkInterface.list(
          includeLinkLocal: false, includeLoopback: false);
      return _epoch.observe(physicalInterfaceSignature(interfaces));
    } catch (_) {
      return _epoch.observe('');
    }
  }

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
    _ownDiagnostic = null;
    _wireGuard?.tunnelDiagnostic = null;
    if (_disposed) return false;
    if (!await _binariesReady()) {
      _ownDiagnostic = const TunnelDiagnostic(
        stage: 'prepare',
        errorCode: 'XRAY_MISSING',
        errorText: '安装不完整：缺少 Xray 组件，请重新安装',
        serviceState: '',
        logSummary: '',
      );
      throw const VpnFailure('XRAY_MISSING', '安装不完整：缺少 Xray 组件，请重新安装');
    }
    if (!await _isAdmin()) {
      _ownDiagnostic = const TunnelDiagnostic(
        stage: 'prepare',
        errorCode: 'PERMISSION_DENIED',
        errorText: '请右键宇海VPN图标，选择「以管理员身份运行」',
        serviceState: '',
        logSummary: '',
      );
      _emitFailure(const VpnFailure('PERMISSION_DENIED', '请右键宇海VPN图标，选择「以管理员身份运行」'));
      return false;
    }
    return true;
  }

  @override
  Future<void> connect(ConnectProfile profile) async {
    _ownDiagnostic = null;
    if (profile is WireGuardProfile && profile.protocol == 'WIREGUARD') {
      if (!_allowWireGuard) {
        throw const FormatException('当前版本的 Windows 客户端请选择出口线路');
      }
      final endpoint = await _pinnedWireGuardEndpoint(profile);
      await _stopXray();
      _mode = _TunnelMode.wireGuard;
      final wireGuard = _wireGuard!..nextEndpointOverride = endpoint;
      await wireGuard.connect(profile);
      return;
    }
    if (profile is! EgressProfile) {
      throw const FormatException('只接受已验证的出口配置');
    }
    if (!profile.expiresAt.isAfter(_now())) {
      throw const VpnFailure('PROFILE_EXPIRED', '连接资料已过期，请重新连接');
    }
    String runtime;
    try {
      runtime = windowsRuntimeConfig(profile.config);
    } on FormatException {
      throw const VpnFailure('PROFILE_INVALID', '线路连接资料无效，请换一条线路或联系客服');
    }
    runtime = await _pinXrayHosts(runtime);
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
    unawaited(spawned.exitCode.then((_) async {
      if (_disposed || generation != _generation || _expectExit) return;
      _expectExit = true;
      _ticker.stop();
      _process = null;
      if (_leakProtect) await holdTraffic();
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
          // 新的 TUN 已经接管默认路由并通过探测，这时再撤掉重连期间的拦截规则。
          _releaseRecoveryGuard();
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
    if (_leakProtect) await holdTraffic();
    if (generation != _generation) return;
    _expectExit = true;
    _ticker.stop();
    _process?.kill();
    _process = null;
    _ownDiagnostic = const TunnelDiagnostic(
      stage: 'handshake',
      errorCode: 'HANDSHAKE_TIMEOUT',
      errorText: '线路握手超时，请重试或更换线路',
      serviceState: '',
      logSummary: '',
    );
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
    // 用户断开：先拆隧道（包括重连途中可能还装着的 WireGuard 隧道服务），最后才撤拦截规则。
    // 重连不走这里：重连只停 xray / 卸隧道服务，规则一直留着。
    final wasWireGuard = _mode == _TunnelMode.wireGuard;
    ++_generation;
    if (!wasWireGuard) _set(VpnStatus.disconnecting);
    await _stopXray();
    _connectedAt = null;
    _mode = _TunnelMode.idle;
    try {
      await _wireGuard?.disconnect();
    } finally {
      await releaseTraffic();
    }
    if (!wasWireGuard) _set(VpnStatus.disconnected);
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
    _guard.release();
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
