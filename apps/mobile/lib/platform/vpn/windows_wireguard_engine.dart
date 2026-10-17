import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import 'vpn_engine.dart';
import 'wireguard_profile.dart';

/// 运行外部程序的抽象，便于测试替换。
typedef ProcessRunner = Future<ProcessResult> Function(
    String executable, List<String> arguments);

/// 设备私钥存放处。Windows 上默认用 flutter_secure_storage（Windows 凭据保护）。
abstract interface class WireGuardKeyStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
}

class SecureKeyStore implements WireGuardKeyStore {
  SecureKeyStore([FlutterSecureStorage? storage])
      : _s = storage ??
            const FlutterSecureStorage(
                mOptions: MacOsOptions(useDataProtectionKeyChain: false));
  final FlutterSecureStorage _s;
  @override
  Future<String?> read(String key) => _s.read(key: key);
  @override
  Future<void> write(String key, String value) =>
      _s.write(key: key, value: value);
}

/// Windows 版 VPN 引擎：用官方 WireGuard for Windows 的「隧道服务」建立连接。
///
/// - 私钥在本机生成，只存 Windows 凭据保护，只有公钥上传服务器。
/// - 连接时写一份仅管理员/SYSTEM 可读的配置到 %ProgramData%\YuhaiVPN\yuhai.conf，
///   再调用 `wireguard.exe /installtunnelservice`；断开时卸载服务并抹掉配置文件。
/// - 只有看到「本次连接之后」的真实握手才报告已连接，统计取自 `wg.exe show yuhai dump`。
/// - 需要以管理员身份运行（安装包的程序清单已声明）。
class WindowsWireGuardEngine implements WireGuardVpnEngine {
  WindowsWireGuardEngine({
    ProcessRunner? run,
    WireGuardKeyStore? keys,
    String? programDataDir,
    String wireGuardDir = r'C:\Program Files\WireGuard',
    DateTime Function()? now,
    Duration pollInterval = const Duration(seconds: 1),
    Duration handshakeTimeout = const Duration(seconds: 30),
  })  : _run = run ?? Process.run,
        _keys = keys ?? SecureKeyStore(),
        _dataDir = programDataDir ??
            '${Platform.environment['ProgramData'] ?? r'C:\ProgramData'}\\YuhaiVPN',
        _wgDir = wireGuardDir,
        _now = now ?? DateTime.now,
        _pollInterval = pollInterval,
        _handshakeTimeout = handshakeTimeout;

  static const tunnelName = 'yuhai';
  final ProcessRunner _run;
  final WireGuardKeyStore _keys;
  final String _dataDir;
  final String _wgDir;
  final DateTime Function() _now;
  final Duration _pollInterval;
  final Duration _handshakeTimeout;

  final _status = StreamController<VpnStatus>.broadcast();
  final _stats = StreamController<VpnStats>.broadcast();
  final _failures = StreamController<VpnFailure>.broadcast();
  int _generation = 0;
  bool _disposed = false;
  Timer? _poll;
  DateTime? _startedAt;
  DateTime? _connectedAt;
  int _lastRx = 0, _lastTx = 0;
  DateTime? _lastSample;

  String get _wireguardExe => '$_wgDir\\wireguard.exe';
  String get _wgExe => '$_wgDir\\wg.exe';
  String get _configPath => '$_dataDir\\$tunnelName.conf';

  @override
  bool get canProbe => !_disposed;
  @override
  Stream<VpnStatus> get status => _status.stream;
  @override
  Stream<VpnStats> get stats => _stats.stream;
  @override
  Stream<VpnFailure> get failures => _failures.stream;

  void _set(VpnStatus s) {
    if (_disposed) return;
    _status.add(s);
  }

  void _fail(String code, String message) {
    if (_disposed) return;
    _poll?.cancel();
    _failures.add(VpnFailure(code, message));
    _set(VpnStatus.error);
  }

  // ---------------- 密钥 ----------------

  static String _keyName(String identity) => 'wg-private:$identity';

  @override
  Future<String> publicKey(String identity) async {
    if (!validVpnIdentity(identity)) throw const FormatException('VPN 身份无效');
    final pair = await _keyPair(identity);
    final pub = await pair.extractPublicKey();
    final encoded = base64Encode(pub.bytes);
    if (!validWireGuardPublicKey(encoded)) {
      throw const FormatException('VPN 设备公钥无效');
    }
    return encoded;
  }

  Future<SimpleKeyPair> _keyPair(String identity) async {
    final algo = X25519();
    final stored = await _keys.read(_keyName(identity));
    if (stored != null) {
      final seed = base64Decode(stored);
      if (seed.length == 32) return algo.newKeyPairFromSeed(seed);
    }
    final pair = await algo.newKeyPair();
    final seed = await pair.extractPrivateKeyBytes();
    await _keys.write(_keyName(identity), base64Encode(seed));
    return algo.newKeyPairFromSeed(seed);
  }

  // ---------------- 准备 ----------------

  @override
  Future<bool> prepare() async {
    if (!await File(_wireguardExe).exists() || !await File(_wgExe).exists()) {
      _fail('WIREGUARD_MISSING', '这台电脑还没有安装 WireGuard 组件，请先安装后再连接');
      return false;
    }
    // `net session` 只有管理员才能成功，用来判断是否以管理员身份运行。
    final admin = await _run('net', ['session']);
    if (admin.exitCode != 0) {
      _fail('PERMISSION_DENIED', '请右键宇海图标，选择「以管理员身份运行」');
      return false;
    }
    return true;
  }

  // ---------------- 配置 ----------------

  /// 生成 WireGuard 配置文本（包含私钥，只写入受保护目录，绝不记录日志）。
  static String buildConfig(
      {required String privateKey, required WireGuardProfile profile}) {
    final b = StringBuffer()
      ..writeln('[Interface]')
      ..writeln('PrivateKey = $privateKey')
      ..writeln('Address = ${profile.address}')
      ..writeln('DNS = ${profile.dns.join(', ')}')
      ..writeln('MTU = ${profile.mtu}')
      ..writeln()
      ..writeln('[Peer]')
      ..writeln('PublicKey = ${profile.serverPublicKey}')
      ..writeln('AllowedIPs = 0.0.0.0/0, ::/0')
      ..writeln('Endpoint = ${profile.endpoint}')
      ..writeln('PersistentKeepalive = 25');
    return b.toString();
  }

  Future<void> _writeConfig(String text) async {
    final dir = Directory(_dataDir);
    if (!await dir.exists()) {
      await dir.create(recursive: true);
    }
    // 目录只允许 SYSTEM 与管理员访问（去掉继承来的 Users 读权限）。
    await _run('icacls', [
      _dataDir,
      '/inheritance:r',
      '/grant:r',
      '*S-1-5-18:(OI)(CI)F',
      '/grant:r',
      '*S-1-5-32-544:(OI)(CI)F'
    ]);
    await File(_configPath).writeAsString(text, flush: true);
  }

  Future<void> _wipeConfig() async {
    final f = File(_configPath);
    try {
      if (await f.exists()) {
        final len = await f.length();
        await f.writeAsBytes(List<int>.filled(len, 0), flush: true);
        await f.delete();
      }
    } catch (_) {
      // 抹除失败不影响断开；下次连接会覆盖。
    }
  }

  Future<bool> _serviceInstalled() async {
    final r = await _run('sc', ['query', 'WireGuardTunnel\$$tunnelName']);
    return r.exitCode == 0;
  }

  // ---------------- 连接 / 断开 ----------------

  @override
  Future<void> connect(ConnectProfile profile) async {
    if (profile is! WireGuardProfile || profile.protocol != 'WIREGUARD') {
      throw const FormatException('只接受已验证的 WireGuard 配置');
    }
    if (!profile.expiresAt.isAfter(_now())) {
      throw const VpnFailure('PROFILE_EXPIRED', '连接资料已过期，请重新连接');
    }
    final generation = ++_generation;
    _poll?.cancel();
    _startedAt = _now();
    _connectedAt = null;
    _lastSample = null;
    _set(VpnStatus.connecting);
    try {
      final pair = await _keyPair(profile.identity);
      final seed = await pair.extractPrivateKeyBytes();
      if (await _serviceInstalled()) {
        await _run(_wireguardExe, ['/uninstalltunnelservice', tunnelName]);
      }
      await _writeConfig(
          buildConfig(privateKey: base64Encode(seed), profile: profile));
      final r = await _run(_wireguardExe, ['/installtunnelservice', _configPath]);
      if (generation != _generation) return;
      if (r.exitCode != 0) {
        await _wipeConfig();
        _fail('CONNECT_FAILED', '无法启动 WireGuard 隧道服务，请确认以管理员身份运行');
        return;
      }
    } catch (_) {
      if (generation == _generation) {
        await _wipeConfig();
        _fail('CONNECT_FAILED', 'VPN 连接失败，请重试');
      }
      return;
    }
    _poll = Timer.periodic(_pollInterval, (_) => unawaited(_sample(generation)));
  }

  /// 解析 `wg show <iface> dump` 的 peer 行：
  /// public-key preshared-key endpoint allowed-ips latest-handshake rx tx keepalive
  static ({int handshake, int rx, int tx})? parseDump(String dump) {
    final lines = const LineSplitter()
        .convert(dump)
        .where((l) => l.trim().isNotEmpty)
        .toList();
    if (lines.length < 2) return null;
    final f = lines[1].split('\t');
    if (f.length < 8) return null;
    final hs = int.tryParse(f[4]);
    final rx = int.tryParse(f[5]);
    final tx = int.tryParse(f[6]);
    if (hs == null || rx == null || tx == null || hs < 0 || rx < 0 || tx < 0) {
      return null;
    }
    return (handshake: hs, rx: rx, tx: tx);
  }

  Future<void> _sample(int generation) async {
    if (_disposed || generation != _generation) return;
    final r = await _run(_wgExe, ['show', tunnelName, 'dump']);
    if (generation != _generation) return;
    final now = _now();
    final d = r.exitCode == 0 ? parseDump(r.stdout.toString()) : null;
    final started = _startedAt ?? now;
    if (_connectedAt == null) {
      final fresh = d != null &&
          d.handshake > 0 &&
          !DateTime.fromMillisecondsSinceEpoch(d.handshake * 1000)
              .isBefore(started.subtract(const Duration(seconds: 1)));
      if (fresh) {
        _connectedAt = now;
        _lastRx = d.rx;
        _lastTx = d.tx;
        _lastSample = now;
        _set(VpnStatus.connected);
      } else if (now.difference(started) > _handshakeTimeout) {
        await disconnect();
        if (generation + 1 == _generation) {
          _fail('HANDSHAKE_TIMEOUT', '线路握手超时，请重试或更换线路');
        }
      }
      return;
    }
    if (d == null) return;
    final last = _lastSample ?? now;
    final secs = now.difference(last).inMilliseconds / 1000.0;
    final up = secs > 0 ? ((d.tx - _lastTx) / secs).round() : 0;
    final down = secs > 0 ? ((d.rx - _lastRx) / secs).round() : 0;
    _lastRx = d.rx;
    _lastTx = d.tx;
    _lastSample = now;
    if (!_disposed) {
      _stats.add(VpnStats(
          upBytes: d.tx,
          downBytes: d.rx,
          upSpeed: up < 0 ? 0 : up,
          downSpeed: down < 0 ? 0 : down,
          since: _connectedAt!.toUtc()));
    }
  }

  @override
  Future<void> disconnect() async {
    ++_generation;
    _poll?.cancel();
    _poll = null;
    _set(VpnStatus.disconnecting);
    try {
      if (await _serviceInstalled()) {
        final r =
            await _run(_wireguardExe, ['/uninstalltunnelservice', tunnelName]);
        if (r.exitCode != 0) {
          _fail('DISCONNECT_FAILED', '断开未获确认，请重试');
          return;
        }
      }
    } finally {
      await _wipeConfig();
    }
    _connectedAt = null;
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
      final s = await Socket.connect(host, port, timeout: timeout);
      watch.stop();
      s.destroy();
      return watch.elapsedMilliseconds;
    } catch (_) {
      return null;
    }
  }

  void dispose() {
    if (_disposed) return;
    unawaited(disconnect().catchError((Object _) {}));
    _disposed = true;
    _poll?.cancel();
    unawaited(_status.close());
    unawaited(_stats.close());
    unawaited(_failures.close());
  }
}
