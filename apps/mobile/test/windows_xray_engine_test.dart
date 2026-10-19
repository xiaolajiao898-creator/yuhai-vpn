import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/egress_profile.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_job.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_config.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_process.dart';

import 'egress_profile_test.dart'
    show egressConfig, egressResponse, parseEgressProfile;
import 'wireguard_profile_test.dart' show parseProfile, profileResponse;

class _Writer implements ConfigWriter {
  final bytes = BytesBuilder();
  var closed = false;

  @override
  void add(List<int> data) => bytes.add(data);

  @override
  Future<void> close() async {
    closed = true;
  }

  String get text => utf8.decode(bytes.toBytes());
}

class _FakeXray {
  _Writer? stdin;
  var killed = false;
  var launches = 0;
  List<String>? arguments;
  String? executable;
  String? workingDirectory;
  final exit = Completer<int>();

  Future<SpawnedXray> launch({
    required String executable,
    required List<String> arguments,
    required String workingDirectory,
  }) async {
    launches++;
    this.executable = executable;
    this.arguments = arguments;
    this.workingDirectory = workingDirectory;
    final writer = _Writer();
    stdin = writer;
    return SpawnedXray(
      pid: 4242,
      stdin: writer,
      exitCode: exit.future,
      kill: () {
        killed = true;
        if (!exit.isCompleted) exit.complete(-1);
      },
    );
  }
}

class _Supervisor implements ProcessSupervisor {
  final pids = <int>[];
  Object? failure;
  var released = false;

  @override
  Future<void> adopt(int pid) async {
    if (failure != null) throw failure!;
    pids.add(pid);
  }

  @override
  Future<void> release() async {
    released = true;
  }
}

class _Ticker implements SampleTicker {
  Future<void> Function()? tick;

  @override
  void start(Duration interval, Future<void> Function() tick) {
    this.tick = tick;
  }

  @override
  void stop() {
    tick = null;
  }

  Future<void> fire() => tick?.call() ?? Future<void>.value();
}

class _Keys implements WireGuardKeyStore {
  @override
  Future<String?> read(String key) async => null;

  @override
  Future<void> write(String key, String value) async {}
}

class _RecordingWireGuard extends WindowsWireGuardEngine {
  _RecordingWireGuard()
      : super(
            keys: _Keys(),
            run: (executable, arguments) async => ProcessResult(0, 0, '', ''));

  var connects = 0;
  var disconnects = 0;

  @override
  Future<void> connect(ConnectProfile profile) async {
    connects++;
  }

  @override
  Future<void> disconnect() async {
    disconnects++;
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  group('Windows Xray 配置', () {
    test('补上 Windows TUN 字段，mtu 仍是单个数字，不改凭据', () {
      final runtime = windowsRuntimeConfig(jsonEncode(egressConfig()));
      final root = jsonDecode(runtime) as Map<String, dynamic>;
      final settings = (((root['inbounds'] as List).single
              as Map)['settings']) as Map;
      expect(settings['mtu'], 1500);
      expect(settings['mtu'], isA<int>());
      expect(settings['name'], 'xray0');
      expect(settings['gateway'], windowsTunGateway);
      expect(settings['dns'], windowsTunDns);
      expect(settings['autoSystemRoutingTable'], windowsTunRoutes);
      expect(settings['autoOutboundsInterface'], 'auto');
      expect(runtime, contains('HTTPPASSWORD'));
      expect(runtime.toLowerCase(), isNot(contains('"listen"')));
      expect(windowsRuntimeInterfaceName(runtime), 'xray0');
    });

    test('危险形状、mtu 数组和未知顶层字段一律拒绝，异常里不带凭据', () {
      final base = egressConfig();
      String encoded(void Function(Map<String, dynamic> cfg) edit) {
        final cfg = jsonDecode(jsonEncode(base)) as Map<String, dynamic>;
        edit(cfg);
        return jsonEncode(cfg);
      }

      final rejected = [
        encoded((cfg) => ((cfg['inbounds'] as List).single
            as Map)['settings']['mtu'] = [1500, 1280]),
        encoded((cfg) => ((cfg['inbounds'] as List).single
            as Map)['settings']['mtu'] = 1500.5),
        encoded((cfg) => (cfg['inbounds'] as List).single['listen'] = '0.0.0.0'),
        encoded((cfg) => cfg['api'] = {'tag': 'api'}),
        encoded((cfg) => cfg['log'] = {'loglevel': 'warning', 'access': 'a.log'}),
        encoded((cfg) {
          final outbound = (cfg['outbounds'] as List).first as Map;
          outbound['streamSettings'] = {
            'network': 'raw',
            'sockopt': {'dialerProxy': 'direct'}
          };
        }),
        encoded((cfg) => (cfg['outbounds'] as List).first['tag'] = 'other'),
        encoded((cfg) => (cfg['outbounds'] as List).first['protocol'] = 'wireguard'),
        '{"allowInsecure":true,"password":"SECRET-VALUE"}',
      ];
      for (final config in rejected) {
        expect(config, isNot(jsonEncode(base)));
        expect(
            () => windowsRuntimeConfig(config),
            throwsA(isA<FormatException>().having(
                (e) => e.toString(), 'text', isNot(contains('SECRET-VALUE')))));
      }
    });
  });

  group('WindowsXrayEngine', () {
    late _FakeXray fake;
    late _Supervisor supervisor;
    late _Ticker ticker;
    late List<VpnStatus> statuses;
    late List<VpnFailure> failures;
    late List<VpnStats> stats;
    var probeResult = true;
    var now = DateTime.utc(2026, 10, 17);

    WindowsXrayEngine build({
      Duration handshakeTimeout = const Duration(seconds: 25),
      int recheckEvery = 60,
      _RecordingWireGuard? wireGuard,
      bool allowWireGuard = false,
      Future<bool> Function()? binariesReady,
      Future<bool> Function()? isAdmin,
    }) {
      final engine = WindowsXrayEngine(
        launch: fake.launch,
        probe: () async => probeResult,
        readStats: (name) async {
          expect(name, 'xray0');
          return (rx: 2048, tx: 512);
        },
        supervisor: supervisor,
        isAdmin: isAdmin ?? () async => true,
        binariesReady: binariesReady ?? () async => true,
        binaryDirectory: r'C:\YuhaiVPN',
        ticker: ticker,
        wireGuard: wireGuard,
        allowWireGuard: allowWireGuard,
        now: () => now,
        handshakeTimeout: handshakeTimeout,
        recheckEvery: recheckEvery,
      );
      statuses = [];
      failures = [];
      stats = [];
      engine.status.listen(statuses.add);
      engine.failures.listen(failures.add);
      engine.stats.listen(stats.add);
      return engine;
    }

    setUp(() {
      fake = _FakeXray();
      supervisor = _Supervisor();
      ticker = _Ticker();
      probeResult = true;
      now = DateTime.utc(2026, 10, 17);
    });

    EgressProfile profile() => parseEgressProfile(egressResponse());

    test('配置只进标准输入，探测成功后才算已连接，统计不另开端口', () async {
      probeResult = false;
      final engine = build();
      await engine.connect(profile());
      expect(statuses, [VpnStatus.connecting]);
      expect(fake.launches, 1);
      expect(fake.arguments, ['run', '-c', 'stdin:']);
      expect(fake.executable, r'C:\YuhaiVPN\xray.exe');
      expect(fake.workingDirectory, r'C:\YuhaiVPN');
      expect(fake.stdin!.closed, isTrue);
      expect(supervisor.pids, [4242]);
      final sent = jsonDecode(fake.stdin!.text) as Map<String, dynamic>;
      final settings =
          (((sent['inbounds'] as List).single as Map)['settings']) as Map;
      expect(settings['mtu'], 1500);
      expect(settings['gateway'], windowsTunGateway);
      expect(fake.stdin!.text, contains('HTTPPASSWORD'));
      expect(fake.arguments!.join(' '), isNot(contains('HTTPPASSWORD')));
      expect(fake.arguments!.join(' '), isNot(contains('api')));

      probeResult = true;
      await ticker.fire();
      expect(statuses, [VpnStatus.connecting, VpnStatus.connected]);
      await ticker.fire();
      expect(stats, isNotEmpty);
      expect(stats.single.downBytes, 2048);
      expect(stats.single.upBytes, 512);
      expect(stats.single.downSpeed, 0);
      engine.dispose();
      expect(supervisor.released, isTrue);
      expect(fake.killed, isTrue);
    });

    test('探测一直失败则超时并结束进程，不报告已连接', () async {
      probeResult = false;
      final engine = build(handshakeTimeout: Duration.zero);
      await engine.connect(profile());
      expect(statuses, contains(VpnStatus.error));
      expect(statuses, isNot(contains(VpnStatus.connected)));
      expect(failures.single.code, 'HANDSHAKE_TIMEOUT');
      expect(fake.killed, isTrue);
      engine.dispose();
    });

    test('每分钟复核失败后进入重连，恢复后再次确认', () async {
      final engine = build(recheckEvery: 1);
      await engine.connect(profile());
      expect(statuses.last, VpnStatus.connected);
      probeResult = false;
      await ticker.fire();
      expect(statuses.last, VpnStatus.reconnecting);
      probeResult = true;
      await ticker.fire();
      expect(statuses.last, VpnStatus.connected);
      engine.dispose();
    });

    test('进程自己退出时报网络不可用', () async {
      final engine = build();
      await engine.connect(profile());
      final pending = expectLater(
          engine.failures,
          emits(isA<VpnFailure>()
              .having((failure) => failure.code, 'code', 'NETWORK_UNAVAILABLE')));
      fake.exit.complete(1);
      await pending;
      engine.dispose();
    });

    test('作业纳管失败时不把配置写进进程', () async {
      supervisor.failure = StateError('job');
      final engine = build();
      await expectLater(
          engine.connect(profile()),
          throwsA(isA<VpnFailure>()
              .having((failure) => failure.code, 'code', 'CONNECT_FAILED')));
      expect(fake.killed, isTrue);
      expect(fake.stdin!.bytes.isEmpty, isTrue);
      expect(fake.stdin!.closed, isFalse);
      engine.dispose();
    });

    test('mtu 数组和过期资料不会启动进程', () async {
      final engine = build();
      final cfg = egressConfig();
      (((cfg['inbounds'] as List).single as Map)['settings'] as Map)['mtu'] = [
        1500
      ];
      await expectLater(
          engine.connect(parseEgressProfile(egressResponse(patch: {'config': cfg}))),
          throwsA(isA<VpnFailure>()
              .having((failure) => failure.code, 'code', 'PROFILE_INVALID')));
      now = DateTime.utc(2026, 11, 1);
      await expectLater(
          engine.connect(profile()),
          throwsA(isA<VpnFailure>()
              .having((failure) => failure.code, 'code', 'PROFILE_EXPIRED')));
      expect(fake.launches, 0);
      engine.dispose();
    });

    test('没有 WireGuard 节点时不把 WireGuard 配置交给 xray，有节点时才委托原引擎', () async {
      final blocked = build();
      await expectLater(
          blocked.connect(parseEgressProfile(egressResponse())),
          completes);
      expect(fake.launches, 1);
      await expectLater(
          blocked.connect(parseProfile(profileResponse())), throwsFormatException);
      expect(fake.launches, 1);
      blocked.dispose();

      fake = _FakeXray();
      final wireGuard = _RecordingWireGuard();
      final delegated = build(wireGuard: wireGuard, allowWireGuard: true);
      await delegated.connect(parseProfile(profileResponse()));
      expect(wireGuard.connects, 1);
      expect(fake.launches, 0);
      await delegated.disconnect();
      expect(wireGuard.disconnects, 1);
      delegated.dispose();
    });

    test('缺少组件或没有管理员权限时准备失败', () async {
      final missing = build(binariesReady: () async => false);
      await expectLater(
          missing.prepare(),
          throwsA(isA<VpnFailure>()
              .having((failure) => failure.code, 'code', 'XRAY_MISSING')));
      missing.dispose();
      final denied = build(isAdmin: () async => false);
      expect(await denied.prepare(), isFalse);
      expect(failures.single.code, 'PERMISSION_DENIED');
      denied.dispose();
    });
  });

  test('Windows 上 Job Object 能在句柄关闭时结束子进程', () async {
    if (!Platform.isWindows) return;
    final supervisor = WindowsJobSupervisor();
    // ping -t 不会自己结束。关掉 Job 句柄后它必须马上退出；Windows 上报的退出码可以是 0。
    final process = await Process.start('cmd.exe', ['/c', 'ping', '-t', '127.0.0.1']);
    try {
      await supervisor.adopt(process.pid);
      await supervisor.release();
      await process.exitCode.timeout(const Duration(seconds: 10));
    } finally {
      process.kill();
      await supervisor.release();
    }
  });
}
