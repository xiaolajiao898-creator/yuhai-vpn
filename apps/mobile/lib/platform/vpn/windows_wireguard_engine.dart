import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

import '../../core/storage/app_secure_storage.dart';
import 'tunnel_diagnostic.dart';
import 'tunnel_host_pins.dart';
import 'vpn_engine.dart';
import 'windows_system_tools.dart';
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
      : _s = storage ?? appSecureStorage;
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
class WindowsWireGuardEngine
    implements WireGuardVpnEngine, HandshakeReporter {
  WindowsWireGuardEngine({
    ProcessRunner? run,
    WireGuardKeyStore? keys,
    String? programDataDir,
    String wireGuardDir = r'C:\Program Files\WireGuard',
    DateTime Function()? now,
    Duration pollInterval = const Duration(seconds: 1),
    Duration handshakeTimeout = const Duration(seconds: 30),
    Future<bool> Function()? ipv6Enabled,
    Future<List<InternetAddress>> Function(String host)? resolveHost,
  })  : _run = run ?? Process.run,
        _keys = keys ?? SecureKeyStore(),
        _dataDir = programDataDir ??
            '${Platform.environment['ProgramData'] ?? r'C:\ProgramData'}\\YuhaiVPN',
        _wgDir = wireGuardDir,
        _now = now ?? DateTime.now,
        _pollInterval = pollInterval,
        _handshakeTimeout = handshakeTimeout,
        _ipv6Enabled = ipv6Enabled,
        _resolveHost = resolveHost;

  static const tunnelName = 'yuhai';
  final ProcessRunner _run;
  final WireGuardKeyStore _keys;
  final String _dataDir;
  final String _wgDir;
  final DateTime Function() _now;
  final Duration _pollInterval;
  final Duration _handshakeTimeout;
  final Future<bool> Function()? _ipv6Enabled;
  final Future<List<InternetAddress>> Function(String host)? _resolveHost;

  final _status = StreamController<VpnStatus>.broadcast();
  final _stats = StreamController<VpnStats>.broadcast();
  final _failures = StreamController<VpnFailure>.broadcast();
  int _generation = 0;
  bool _disposed = false;
  Timer? _poll;
  bool _sampling = false;
  DateTime? _startedAt;
  DateTime? _connectedAt;
  int _lastRx = 0, _lastTx = 0;
  DateTime? _lastSample;

  String get _wireguardExe => '$_wgDir\\wireguard.exe';

  /// 隧道服务的程序路径（断网保护的放行名单要用它：握手包由这个进程发出）。
  String get wireguardExePath => _wireguardExe;

  @override
  bool get reportsHandshake => !_disposed;

  @override
  TunnelDiagnostic? tunnelDiagnostic;

  /// 握手超时、即将卸载隧道服务之前调用。WireGuard 服务自带的防火墙会随服务一起消失，
  /// 断网保护要在这之前先把自己的拦截规则装好，中间不能留空档。
  Future<void> Function()? beforeTimeoutTeardown;
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
    final admin = await _run(windowsSystem32Tool('net.exe'), ['session']);
    if (admin.exitCode != 0) {
      _fail('PERMISSION_DENIED', '请右键宇海VPN图标，选择「以管理员身份运行」');
      return false;
    }
    return true;
  }

  // ---------------- 配置 ----------------

  /// 生成 WireGuard 配置文本（包含私钥，只写入受保护目录，绝不记录日志）。
  ///
  /// [endpoint] 只用于断网保护拦着、系统查不到服务器域名时，换成之前查到的 `IP:端口`
  /// （见 tunnel_host_pins.dart）。不是 IP 字面量的一律不用，仍写资料里的 endpoint。
  static String buildConfig(
      {required String privateKey,
      required WireGuardProfile profile,
      String? endpoint,
      bool ipv6 = true}) {
    final host = endpoint == null ? null : wireGuardEndpointHost(endpoint);
    final peer = host != null && InternetAddress.tryParse(host) != null
        ? endpoint!
        : profile.endpoint;
    final dns = _windowsDns(profile.dns);
    // 1280 能过 PPPoE 和手机网络。握手包很小，MTU 不决定能不能握手，但过高时隧道起来后仍像没连上。
    // 没有 IPv6 时写 ::/0 会让隧道服务直接退出。
    final allowed = ipv6 ? '0.0.0.0/0, ::/0' : '0.0.0.0/0';
    final b = StringBuffer()
      ..writeln('[Interface]')
      ..writeln('PrivateKey = $privateKey')
      ..writeln('Address = ${profile.address}')
      ..writeln('DNS = ${dns.join(', ')}')
      ..writeln('MTU = 1280')
      ..writeln()
      ..writeln('[Peer]')
      ..writeln('PublicKey = ${profile.serverPublicKey}')
      ..writeln('AllowedIPs = $allowed')
      ..writeln('Endpoint = $peer')
      ..writeln('PersistentKeepalive = 25');
    return b.toString();
  }

  static List<String> _windowsDns(List<String> dns) {
    final v4 = dns.where((item) => InternetAddress.tryParse(item)?.type == InternetAddressType.IPv4).toList();
    final chosen = v4.isNotEmpty ? v4 : dns;
    return chosen.take(4).toList();
  }

  /// 只信任 SYSTEM（S-1-5-18）与本机 Administrators 组（S-1-5-32-544）。
  static const _trustedSids = {'S-1-5-18', 'S-1-5-32-544'};

  static const _untrustedDir = VpnFailure('CONFIG_DIR_UNTRUSTED',
      '配置目录的所有者或权限不安全，已拒绝写入私钥。请删除 %ProgramData%\\YuhaiVPN 文件夹后重试，或联系客服');

  /// 读取目录的所有者与全部访问规则（均以 SID 表示，不受系统语言影响）。
  /// 第一行是所有者 SID，其后每行一条访问规则的 SID。读取失败返回 null。
  Future<List<String>?> _ownerAndAceSids(String path) async {
    final quoted = "'${path.replaceAll("'", "''")}'";
    final r = await _run(windowsPowerShellExe(), [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      '\$ErrorActionPreference = "Stop"; '
          '\$sid = [System.Security.Principal.SecurityIdentifier]; '
          '\$a = Get-Acl -LiteralPath $quoted; '
          'Write-Output \$a.GetOwner(\$sid).Value; '
          'foreach (\$r in \$a.GetAccessRules(\$true, \$true, \$sid)) { Write-Output \$r.IdentityReference.Value }',
    ]);
    if (r.exitCode != 0) return null;
    final lines = r.stdout
        .toString()
        .split(RegExp(r'\r?\n'))
        .map((l) => l.trim())
        .where((l) => l.isNotEmpty)
        .toList();
    if (lines.isEmpty || lines.any((l) => !RegExp(r'^S-1-[0-9-]+$').hasMatch(l))) return null;
    return lines;
  }

  /// 写入含私钥的配置前，确认目录确实只归 SYSTEM/管理员所有和访问。
  /// - 目录是链接（符号链接/联接点）：拒绝。
  /// - 目录已存在但所有者不是 SYSTEM/Administrators（例如普通用户抢先建好的）：拒绝，不去「修复」它。
  /// - 新建的目录显式把所有者设为 Administrators；icacls 每一步都检查退出码。
  /// - 收紧权限后再核对一次：所有者可信，且访问规则里只剩 SYSTEM 与 Administrators。
  Future<void> _writeConfig(String text) async {
    final dir = Directory(_dataDir);
    final icacls = windowsSystem32Tool('icacls.exe');
    if (await FileSystemEntity.isLink(_dataDir)) throw _untrustedDir;
    if (await dir.exists()) {
      final before = await _ownerAndAceSids(_dataDir);
      if (before == null || !_trustedSids.contains(before.first)) throw _untrustedDir;
    } else {
      await dir.create(recursive: true);
      if (await FileSystemEntity.isLink(_dataDir)) throw _untrustedDir;
      final owner = await _run(icacls, [_dataDir, '/setowner', '*S-1-5-32-544']);
      if (owner.exitCode != 0) throw _untrustedDir;
    }
    // 目录只允许 SYSTEM 与管理员访问（去掉继承来的 Users 读权限）。
    final acl = await _run(icacls, [
      _dataDir,
      '/inheritance:r',
      '/grant:r',
      '*S-1-5-18:(OI)(CI)F',
      '/grant:r',
      '*S-1-5-32-544:(OI)(CI)F'
    ]);
    if (acl.exitCode != 0) throw _untrustedDir;
    final after = await _ownerAndAceSids(_dataDir);
    if (after == null || !_trustedSids.contains(after.first) || !after.skip(1).every(_trustedSids.contains)) {
      throw _untrustedDir;
    }
    final file = File(_configPath);
    if (await FileSystemEntity.isLink(_configPath)) throw _untrustedDir;
    // WireGuard for Windows 按 UTF-8 读配置。写成 CRLF、不带 BOM，避免服务把配置判坏后马上退出。
    final bytes = utf8.encode(text.replaceAll('\r\n', '\n').replaceAll('\n', '\r\n'));
    await file.writeAsBytes(bytes, flush: true);
    final fileAcl = await _run(icacls, [
      _configPath,
      '/inheritance:r',
      '/grant:r',
      '*S-1-5-18:F',
      '/grant:r',
      '*S-1-5-32-544:F',
    ]);
    if (fileAcl.exitCode != 0) throw _untrustedDir;
  }

  Future<void> _wipeConfig() async {
    final f = File(_configPath);
    try {
      // 只在可信目录里抹写：所有者不是 SYSTEM/Administrators 的目录里，配置文件可能是
      // 普通用户放的硬链接，以管理员身份往里写零会破坏别处的文件。
      if (await FileSystemEntity.isLink(_dataDir) || await FileSystemEntity.isLink(_configPath)) return;
      if (!await f.exists()) return;
      final owner = await _ownerAndAceSids(_dataDir);
      if (owner == null || !_trustedSids.contains(owner.first)) return;
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
    final r = await _run(windowsSystem32Tool('sc.exe'), ['query', 'WireGuardTunnel\$$tunnelName']);
    return r.exitCode == 0;
  }

  // ---------------- 连接 / 断开 ----------------

  /// 只给 [WindowsXrayEngine] 用：断网保护拦着、系统查不到服务器域名时，下一次 [connect]
  /// 把 endpoint 换成这个之前查到的 `IP:端口`。每次 [connect] 读取后即清空。
  String? nextEndpointOverride;

  @override
  Future<void> connect(ConnectProfile profile) async {
    final endpointOverride = nextEndpointOverride;
    nextEndpointOverride = null;
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
    tunnelDiagnostic = null;
    _set(VpnStatus.connecting);
    try {
      final String privateKey;
      final issued = profile.issuedPrivateKey;
      if (issued != null) {
        privateKey = issued;
      } else {
        final pair = await _keyPair(profile.identity);
        final seed = await pair.extractPrivateKeyBytes();
        privateKey = base64Encode(seed);
      }
      await _quietUninstall();
      await _waitUntilGone();
      await _allowWireGuardFirewall();
      final ipv6 = await (_ipv6Enabled ?? _defaultIPv6)();
      final endpoint = await _preferIPv4Endpoint(endpointOverride ?? profile.endpoint);
      await _writeConfig(buildConfig(
          privateKey: privateKey, profile: profile, endpoint: endpoint, ipv6: ipv6));
      var r = await _run(_wireguardExe, ['/installtunnelservice', _configPath]);
      if (generation != _generation) return;
      if (r.exitCode != 0 && _installAlreadyExists(r)) {
        await _quietUninstall();
        await _waitUntilGone();
        r = await _run(_wireguardExe, ['/installtunnelservice', _configPath]);
      }
      if (generation != _generation) return;
      if (r.exitCode != 0) {
        final detail = '${r.stdout}\n${r.stderr}';
        await _quietUninstall();
        await _wipeConfig();
        final logs = await _collectLogs();
        tunnelDiagnostic = TunnelDiagnostic(
          stage: 'install',
          errorCode: 'CONNECT_FAILED',
          errorText: _installFailureMessage(r.exitCode, detail),
          serviceState: logs.service,
          logSummary: redactTunnelText('install_exit=${r.exitCode}\n$detail\n${logs.log}'),
        );
        _fail('CONNECT_FAILED', tunnelDiagnostic!.errorText);
        return;
      }
      final stopped = await _waitUntilRunning();
      if (generation != _generation) return;
      if (stopped != null) {
        final logs = await _collectLogs();
        await _quietUninstall();
        await _wipeConfig();
        tunnelDiagnostic = TunnelDiagnostic(
          stage: 'service',
          errorCode: 'TUNNEL_SERVICE_STOPPED',
          errorText: _serviceStoppedMessage(stopped),
          serviceState: logs.service,
          logSummary: logs.log,
        );
        _fail('TUNNEL_SERVICE_STOPPED', tunnelDiagnostic!.errorText);
        return;
      }
    } on VpnFailure catch (e) {
      // 目录不可信时不碰里面的任何文件（可能是指向别处的链接）。
      if (generation == _generation) {
        if (e.code != _untrustedDir.code) await _wipeConfig();
        _fail(e.code, e.message);
      }
      return;
    } catch (error) {
      if (generation == _generation) {
        await _wipeConfig();
        final detail = redactTunnelText('$error');
        tunnelDiagnostic = TunnelDiagnostic(
          stage: 'install',
          errorCode: 'CONNECT_FAILED',
          errorText: 'VPN 连接失败，请重试',
          serviceState: '',
          logSummary: detail,
        );
        _fail('CONNECT_FAILED', 'VPN 连接失败，请重试');
      }
      return;
    }
    _poll = Timer.periodic(_pollInterval, (_) => unawaited(_sample(generation)));
    unawaited(_sample(generation));
  }

  bool _installAlreadyExists(ProcessResult result) {
    final text = '${result.stdout}\n${result.stderr}'.toLowerCase();
    return text.contains('already exists') || text.contains('already installed');
  }

  String _installFailureMessage(int exitCode, String detail) {
    final lower = detail.toLowerCase();
    if (lower.contains('access is denied') || detail.contains('拒绝访问')) {
      return '无法启动 WireGuard 隧道服务（退出码 $exitCode）：没有管理员权限。请右键宇海VPN，选择「以管理员身份运行」';
    }
    if (lower.contains('wintun') || lower.contains('adapter') || detail.contains('网卡')) {
      return '无法启动 WireGuard 隧道服务（退出码 $exitCode）：系统拒绝创建网卡。请把 WireGuard 加入杀毒软件和防火墙白名单后重试';
    }
    final clip = redactTunnelText(detail).replaceAll(RegExp(r'\s+'), ' ').trim();
    final extra = clip.isEmpty ? '' : ' ${clip.length > 120 ? clip.substring(0, 120) : clip}';
    return '无法启动 WireGuard 隧道服务（退出码 $exitCode）。请确认以管理员身份运行，并把 WireGuard 加入杀毒软件和防火墙白名单。$extra'
        .trim();
  }

  String _serviceStoppedMessage(String state) =>
      'WireGuard 隧道服务已停止（$state）。杀毒软件或防火墙可能拦截了网卡驱动，请把 WireGuard 加入白名单后重试';

  Future<void> _allowWireGuardFirewall() async {
    const name = 'YuhaiVPN WireGuard';
    final netsh = windowsSystem32Tool('netsh.exe');
    try {
      await _run(netsh, ['advfirewall', 'firewall', 'delete', 'rule', 'name=$name']);
      await _run(netsh, [
        'advfirewall',
        'firewall',
        'add',
        'rule',
        'name=$name',
        'dir=out',
        'action=allow',
        'program=$_wireguardExe',
        'enable=yes',
        'profile=any',
        'protocol=UDP',
      ]);
    } catch (_) {}
  }

  Future<void> _quietUninstall() async {
    try {
      if (await _serviceInstalled()) {
        await _run(_wireguardExe, ['/uninstalltunnelservice', tunnelName]);
      }
    } catch (_) {}
  }

  Future<void> _waitUntilGone() async {
    for (var i = 0; i < 20; i++) {
      if (!await _serviceInstalled()) return;
      await Future<void>.delayed(const Duration(milliseconds: 200));
    }
  }

  Future<bool> _serviceDied() async => await _serviceState() == 'STOPPED';

  Future<String> _serviceState() async {
    final r = await _run(windowsSystem32Tool('sc.exe'), ['query', 'WireGuardTunnel\$$tunnelName']);
    final text = '${r.stdout}\n${r.stderr}';
    final match = RegExp(r'STATE\s+:\s+\d+\s+([A-Z_]+)').firstMatch(text);
    if (match != null) return match.group(1)!;
    if (r.exitCode != 0) return 'ABSENT';
    return 'UNKNOWN';
  }

  /// 返回非 null 表示服务没有进入 RUNNING。START_PENDING 会再等一会儿。
  Future<String?> _waitUntilRunning() async {
    for (var i = 0; i < 10; i++) {
      final state = await _serviceState();
      if (state == 'RUNNING') return null;
      if (state == 'STOPPED') return state;
      await Future<void>.delayed(const Duration(milliseconds: 300));
    }
    final state = await _serviceState();
    return state == 'RUNNING' ? null : state;
  }

  Future<bool> _defaultIPv6() async {
    try {
      final ifaces = await NetworkInterface.list(includeLinkLocal: false, includeLoopback: false);
      for (final iface in ifaces) {
        for (final addr in iface.addresses) {
          if (addr.type == InternetAddressType.IPv6) return true;
        }
      }
    } catch (_) {}
    return false;
  }

  Future<String> _preferIPv4Endpoint(String endpoint) async {
    final host = wireGuardEndpointHost(endpoint);
    if (host == null || InternetAddress.tryParse(host) != null) return endpoint;
    try {
      final found = await (_resolveHost ?? InternetAddress.lookup)(host).timeout(const Duration(seconds: 3));
      for (final addr in found) {
        if (addr.type == InternetAddressType.IPv4) return pinWireGuardEndpoint(endpoint, addr);
      }
    } catch (_) {}
    return endpoint;
  }

  Future<({String service, String log})> _collectLogs() async {
    try {
      final state = await _serviceState();
      final dump = await _run(_wgExe, ['show', tunnelName, 'dump']);
      final dumplog = await _run(_wireguardExe, ['/dumplog']);
      final summary = StringBuffer()
        ..writeln('service=$state')
        ..writeln('wg_exit=${dump.exitCode}')
        ..writeln(dump.stdout)
        ..writeln(dump.stderr)
        ..writeln('dumplog_exit=${dumplog.exitCode}')
        ..writeln(dumplog.stdout)
        ..writeln(dumplog.stderr);
      return (service: state, log: redactTunnelText(summary.toString()));
    } catch (_) {
      return (service: '', log: '');
    }
  }

  Future<void> _teardownQuiet() async {
    ++_generation;
    _poll?.cancel();
    _poll = null;
    await _quietUninstall();
    await _wipeConfig();
    _connectedAt = null;
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
    if (_disposed || generation != _generation || _sampling) return;
    _sampling = true;
    try {
      await _sampleOnce(generation);
    } finally {
      _sampling = false;
    }
  }

  Future<void> _sampleOnce(int generation) async {
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
      } else if (await _serviceDied()) {
        if (generation != _generation) return;
        final logs = await _collectLogs();
        final guard = beforeTimeoutTeardown;
        if (guard != null) {
          try {
            await guard();
          } catch (_) {}
          if (generation != _generation) return;
        }
        await _teardownQuiet();
        if (generation + 1 == _generation) {
          tunnelDiagnostic = TunnelDiagnostic(
            stage: 'service',
            errorCode: 'TUNNEL_SERVICE_STOPPED',
            errorText: _serviceStoppedMessage(logs.service.isEmpty ? 'STOPPED' : logs.service),
            serviceState: logs.service,
            logSummary: logs.log,
          );
          _fail('TUNNEL_SERVICE_STOPPED', tunnelDiagnostic!.errorText);
        }
      } else if (now.difference(started) > _handshakeTimeout) {
        final logs = await _collectLogs();
        final guard = beforeTimeoutTeardown;
        if (guard != null) {
          try {
            await guard();
          } catch (_) {}
          if (generation != _generation) return;
        }
        tunnelDiagnostic = TunnelDiagnostic(
          stage: 'handshake',
          errorCode: 'HANDSHAKE_TIMEOUT',
          errorText: '线路握手超时，请重试或更换线路',
          serviceState: logs.service,
          logSummary: logs.log,
        );
        await _teardownQuiet();
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

  /// 最近一次握手距今多少秒，读不到时返回 null。WireGuard 有流量（这里有 25 秒保活）时
  /// 大约每两分钟重新握手一次；超过 180 秒（Reject-After-Time）没有新握手，这条隧道已经
  /// 不能再传数据。
  Future<int?> handshakeAgeSeconds() async {
    if (_disposed || _connectedAt == null) return null;
    try {
      final r = await _run(_wgExe, ['show', tunnelName, 'dump']);
      if (r.exitCode != 0) return null;
      final d = parseDump(r.stdout.toString());
      if (d == null || d.handshake <= 0) return null;
      final age = _now().millisecondsSinceEpoch ~/ 1000 - d.handshake;
      return age < 0 ? 0 : age;
    } catch (_) {
      return null;
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

  @override
  bool get canRecover => false;
  @override
  Future<void> applyPolicy({required bool leakProtect}) async {}
  @override
  Future<int?> probeTunnel() async => null;
  @override
  Future<bool> holdTraffic() async => false;
  @override
  Future<void> releaseTraffic() async {}
  @override
  Future<int> networkEpoch() async => 0;

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
