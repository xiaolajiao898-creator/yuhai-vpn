import 'dart:convert';
import 'dart:io';

/// xray 标准输入。配置只从这里进去，不落盘。
abstract interface class ConfigWriter {
  void add(List<int> data);
  Future<void> close();
}

class SpawnedXray {
  SpawnedXray({
    required this.pid,
    required this.stdin,
    required this.exitCode,
    required void Function() kill,
  }) : _kill = kill;

  final int pid;
  final ConfigWriter stdin;
  final Future<int> exitCode;
  final void Function() _kill;

  void kill() => _kill();
}

typedef XrayLauncher = Future<SpawnedXray> Function({
  required String executable,
  required List<String> arguments,
  required String workingDirectory,
});

class _SinkWriter implements ConfigWriter {
  _SinkWriter(this._sink);
  final IOSink _sink;

  @override
  void add(List<int> data) => _sink.add(data);

  @override
  Future<void> close() => _sink.close();
}

/// 启动 `xray.exe run -c stdin:`。环境里去掉 `XRAY_*`，避免把别的配置目录并进来。
Future<SpawnedXray> launchXrayProcess({
  required String executable,
  required List<String> arguments,
  required String workingDirectory,
}) async {
  const keep = {
    'SystemRoot',
    'WINDIR',
    'PATH',
    'PATHEXT',
    'TEMP',
    'TMP',
    'USERPROFILE',
    'APPDATA',
    'LOCALAPPDATA',
    'PROGRAMDATA',
    'NUMBER_OF_PROCESSORS',
    'PROCESSOR_ARCHITECTURE',
    'OS',
    'ComSpec',
    'HOMEDRIVE',
    'HOMEPATH',
    'USERNAME',
    'USERDOMAIN',
  };
  final env = <String, String>{};
  for (final key in keep) {
    final value = Platform.environment[key];
    if (value != null) env[key] = value;
  }
  final process = await Process.start(
    executable,
    arguments,
    workingDirectory: workingDirectory,
    environment: env,
    mode: ProcessStartMode.normal,
    runInShell: false,
  );
  // 不读会把管道写满，xray 会停在日志上。内容可能含目标地址，读完即丢。
  process.stdout.listen((_) {}, onError: (_) {}, cancelOnError: false);
  process.stderr.listen((_) {}, onError: (_) {}, cancelOnError: false);
  return SpawnedXray(
    pid: process.pid,
    stdin: _SinkWriter(process.stdin),
    exitCode: process.exitCode,
    kill: () {
      process.kill();
    },
  );
}

/// 经 TUN 源地址发一次真实 HTTPS。本机没有 172.19.0.1 时绑定失败，不能把直连成功当成已连接。
Future<bool> probeTunnelHttps() async {
  const urls = [
    'https://www.gstatic.com/generate_204',
    'https://cp.cloudflare.com/generate_204',
  ];
  for (final raw in urls) {
    final uri = Uri.parse(raw);
    SecureSocket? socket;
    try {
      // 先绑在隧道地址上再升级 TLS。本机没有 172.19.0.1 时这一步失败，
      // 直连成功不能被当成已经连上。
      final plain = await Socket.connect(
        uri.host,
        443,
        sourceAddress: '172.19.0.1',
        timeout: const Duration(seconds: 8),
      ).timeout(const Duration(seconds: 10));
      socket = await SecureSocket.secure(plain, host: uri.host)
          .timeout(const Duration(seconds: 10));
      final path = uri.path.isEmpty ? '/' : uri.path;
      socket.add(utf8.encode(
          'GET $path HTTP/1.1\r\nHost: ${uri.host}\r\nConnection: close\r\nUser-Agent: YuhaiVPN\r\n\r\n'));
      final collected = <int>[];
      await socket.timeout(const Duration(seconds: 10)).forEach((chunk) {
        if (collected.length < 128) collected.addAll(chunk);
      });
      final head = String.fromCharCodes(collected.take(80));
      if (RegExp(r'^HTTP/1\.[01] (204|200)\b').hasMatch(head)) return true;
    } catch (_) {
      // 下一条探测地址。
    } finally {
      try {
        await socket?.close();
      } catch (_) {}
    }
  }
  return false;
}

/// Wintun 计数。ReceivedBytes 是系统从网卡收到的（下行），SentBytes 是系统送进网卡的（上行）。
/// 不打开本机 API 端口。
Future<({int rx, int tx})?> readWindowsAdapterStats(String name) async {
  if (!Platform.isWindows) return null;
  if (!RegExp(r'^[A-Za-z][A-Za-z0-9_-]{0,31}$').hasMatch(name)) return null;
  final result = await Process.run('powershell.exe', [
    '-NoProfile',
    '-NonInteractive',
    '-Command',
    "\$a = Get-NetAdapterStatistics -Name '$name' -ErrorAction SilentlyContinue; "
        'if (\$null -eq \$a) { exit 2 }; '
        'Write-Output ([string]\$a.ReceivedBytes + \' \' + [string]\$a.SentBytes)',
  ]);
  if (result.exitCode != 0) return null;
  final parts = result.stdout.toString().trim().split(RegExp(r'\s+'));
  if (parts.length != 2) return null;
  final rx = int.tryParse(parts[0]);
  final tx = int.tryParse(parts[1]);
  if (rx == null || tx == null || rx < 0 || tx < 0) return null;
  return (rx: rx, tx: tx);
}

Future<bool> windowsIsAdministrator() async {
  if (!Platform.isWindows) return false;
  final result = await Process.run('net', ['session']);
  return result.exitCode == 0;
}

Future<bool> windowsXrayBinariesReady(String directory) async {
  final exe = File('$directory\\xray.exe');
  final dll = File('$directory\\wintun.dll');
  return exe.existsSync() && dll.existsSync();
}

String defaultXrayDirectory() => File(Platform.resolvedExecutable).parent.path;
