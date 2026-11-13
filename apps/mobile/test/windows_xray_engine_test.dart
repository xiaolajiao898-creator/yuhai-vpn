import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/egress_profile.dart';
import 'package:yuhai_vpn/platform/vpn/tunnel_host_pins.dart';
import 'package:yuhai_vpn/platform/vpn/vpn_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_job.dart';
import 'package:yuhai_vpn/platform/vpn/windows_traffic_guard.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_config.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_process.dart';
import 'package:yuhai_vpn/platform/vpn/wireguard_profile.dart';

import 'egress_profile_test.dart'
    show egressConfig, egressResponse, parseEgressProfile;
import 'wireguard_profile_test.dart'
    show clientPublic, parseProfile, profileNow, profileResponse, serverPublic;

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
  final endpoints = <String?>[];

  @override
  Future<void> connect(ConnectProfile profile) async {
    connects++;
    endpoints.add(nextEndpointOverride);
    nextEndpointOverride = null;
  }

  @override
  Future<void> disconnect() async {
    disconnects++;
  }
}

class _HeldGuard extends WindowsTrafficGuard {
  var holding = false;

  @override
  bool get held => holding;

  @override
  Future<bool> hold(List<String> permitPaths) async {
    holding = true;
    return true;
  }

  @override
  void release() {
    holding = false;
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
      TunnelHostMemory? tunnelHosts,
      WindowsTrafficGuard? trafficGuard,
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
        tunnelHosts: tunnelHosts,
        trafficGuard: trafficGuard,
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

    group('断网保护拦着时用记下的服务器地址', () {
      final pinned = InternetAddress('198.51.100.7');

      EgressProfile domainProfile() {
        final config = egressConfig();
        final proxy = (config['outbounds'] as List).first as Map;
        (proxy['settings'] as Map)['address'] = 'egress.example.test';
        return parseEgressProfile(egressResponse(patch: {'config': config}));
      }

      test('没拦着时只记下服务器域名的地址，交给 xray 的配置原样不变', () async {
        final asked = <String>[];
        final memory = TunnelHostMemory(lookup: (host) async {
          asked.add(host);
          return [pinned];
        });
        final engine = build(tunnelHosts: memory);
        final profile = domainProfile();
        await engine.connect(profile);
        await pumpEventQueue();
        expect(fake.stdin!.text, windowsRuntimeConfig(profile.config));
        expect(asked, ['egress.example.test']);
        expect(memory.remembered('egress.example.test'), [pinned]);
        engine.dispose();
      });

      test('没拦着时 WireGuard 照用资料里的 endpoint', () async {
        final wireGuard = _RecordingWireGuard();
        final memory = TunnelHostMemory(lookup: (host) async => [pinned]);
        final engine = build(
            wireGuard: wireGuard, allowWireGuard: true, tunnelHosts: memory);
        await engine.connect(parseProfile(profileResponse()));
        await pumpEventQueue();
        expect(wireGuard.endpoints, [null]);
        expect(memory.remembered('vpn.example.com'), [pinned]);
        engine.dispose();
      });

      // 拦截规则只在 Windows 上装得上（_engageGuard 先看系统），这两条在 Windows 的 CI 上跑。
      test('拦着、查不到域名时，Xray 配置带上记下的 IP，地址和凭据不变', () async {
        var down = false;
        final memory = TunnelHostMemory(lookup: (host) async {
          if (down) throw const SocketException('Failed host lookup');
          return [pinned];
        });
        final engine = build(tunnelHosts: memory, trafficGuard: _HeldGuard());
        await engine.connect(domainProfile());
        await pumpEventQueue();
        down = true;
        expect(await engine.holdTraffic(), isTrue);
        await engine.connect(domainProfile());
        final sent = jsonDecode(fake.stdin!.text) as Map<String, dynamic>;
        expect((sent['dns'] as Map)['hosts'], {
          'egress.example.test': ['198.51.100.7']
        });
        final proxy = (sent['outbounds'] as List).first as Map;
        expect((proxy['settings'] as Map)['address'], 'egress.example.test');
        expect((proxy['streamSettings'] as Map)['sockopt'],
            {'domainStrategy': 'UseIP'});
        expect(fake.stdin!.text, contains('HTTPPASSWORD'));
        engine.dispose();
      }, skip: !Platform.isWindows);

      test('拦着、查不到域名时，WireGuard 的 endpoint 换成记下的 IP', () async {
        var down = false;
        final wireGuard = _RecordingWireGuard();
        final memory = TunnelHostMemory(lookup: (host) async {
          if (down) throw const SocketException('Failed host lookup');
          return [pinned];
        });
        final engine = build(
            wireGuard: wireGuard,
            allowWireGuard: true,
            tunnelHosts: memory,
            trafficGuard: _HeldGuard());
        await engine.connect(parseProfile(profileResponse()));
        await pumpEventQueue();
        down = true;
        expect(await engine.holdTraffic(), isTrue);
        await engine.connect(parseProfile(profileResponse()));
        expect(wireGuard.endpoints, [null, '198.51.100.7:51820']);
        engine.dispose();
      }, skip: !Platform.isWindows);

      // 批发线路：线路编号 w43，服务器编号 43，endpoint 是域名。
      WireGuardProfile issuedProfile() {
        final now = DateTime.utc(2026, 11, 10, 1);
        return WireGuardProfile.parseIssued({
          'line_id': 'w43',
          'server_id': '43',
          'expires_at': now.add(const Duration(minutes: 5)).toIso8601String(),
          'endpoint': 'vpn.example.com:51820',
          'server_public_key': serverPublic,
          'address': '10.0.0.2/32',
          'dns': ['1.1.1.1'],
          'mtu': 1280,
          'interface_private_key': base64Encode(List<int>.filled(32, 3)),
        }, lineId: 'w43', identity: '8302571644:123', now: now);
      }

      test('刷新线路列表时只预先查域名，IP 不查；上一轮还没查完不重复查', () async {
        final asked = <String>[];
        final gate = Completer<void>();
        final memory = TunnelHostMemory(lookup: (host) async {
          asked.add(host);
          await gate.future;
          return [pinned];
        });
        final engine = build(tunnelHosts: memory);
        engine.noteLineHosts({
          'e2': 'egress.example.test',
          'w43': '198.51.100.9',
          '7': 'node.example.test',
        });
        engine.noteLineHosts({'e3': 'other.example.test'});
        gate.complete();
        await pumpEventQueue();
        expect(asked, ['egress.example.test', 'node.example.test']);
        expect(memory.remembered('node.example.test'), [pinned]);
        engine.noteLineHosts({'e3': 'other.example.test'});
        await pumpEventQueue();
        expect(asked.last, 'other.example.test');
        engine.dispose();
      });

      test('没连过的出口线路：列表刷新时预先查过，拦着时也能换成 IP', () async {
        var down = false;
        final memory = TunnelHostMemory(lookup: (host) async {
          if (down) throw const SocketException('Failed host lookup');
          return [pinned];
        });
        final engine = build(tunnelHosts: memory, trafficGuard: _HeldGuard());
        engine.noteLineHosts({'e2': 'egress.example.test'});
        await pumpEventQueue();
        down = true;
        expect(await engine.holdTraffic(), isTrue);
        await engine.connect(domainProfile());
        final sent = jsonDecode(fake.stdin!.text) as Map<String, dynamic>;
        expect((sent['dns'] as Map)['hosts'], {
          'egress.example.test': ['198.51.100.7']
        });
        engine.dispose();
      }, skip: !Platform.isWindows);

      test('没连过的批发线路：endpoint 查不到也没记过时，用列表里的服务器 IP', () async {
        final wireGuard = _RecordingWireGuard();
        final memory = TunnelHostMemory(
            lookup: (host) async =>
                throw const SocketException('Failed host lookup'));
        final engine = build(
            wireGuard: wireGuard,
            allowWireGuard: true,
            tunnelHosts: memory,
            trafficGuard: _HeldGuard());
        engine.noteLineHosts({'w43': '198.51.100.9'});
        expect(await engine.holdTraffic(), isTrue);
        await engine.connect(issuedProfile());
        // 自建线路的编号也可能是 43：不能拿批发线路 w43 的 IP 去连它。
        final selfHosted = profileResponse();
        ((selfHosted['profiles'] as List).first as Map)['node_id'] = '43';
        await engine.connect(WireGuardProfile.parseResponse(selfHosted,
            nodeId: '43',
            identity: '8302571644:123',
            clientPublicKey: clientPublic,
            now: profileNow));
        expect(wireGuard.endpoints, ['198.51.100.9:51820', null]);
        engine.dispose();
      }, skip: !Platform.isWindows);
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
