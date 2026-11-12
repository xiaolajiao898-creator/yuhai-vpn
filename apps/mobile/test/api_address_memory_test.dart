import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/network/api_address_memory.dart';
import 'package:yuhai_vpn/core/network/api_client.dart';
import 'package:yuhai_vpn/core/security/device_key.dart';
import 'package:yuhai_vpn/core/storage/session_store.dart';

/// 项目的 test/flutter_test_config.dart 会初始化测试绑定，绑定把 HttpOverrides.global
/// 换成一律回 400、不真连网的假客户端。这些测试要真的连本机的服务，所以在每个测试的
/// zone 里换回 dart:io 自带的 HttpClient（zone 里的设置优先于 global）。
class _RealHttp extends HttpOverrides {}

void _test(String name, Future<void> Function() body) =>
    test(name, () => HttpOverrides.runWithHttpOverrides(body, _RealHttp()));

/// Windows 断网保护拦着时系统查不了域名：API 改用上次查到的地址直连。
/// 这里用本机的 HTTP 服务模拟服务端，用假的 lookup 模拟「查不到域名」。
void main() {
  late HttpServer server;
  late int requests;

  setUp(() async {
    requests = 0;
    server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((request) async {
      requests++;
      await request.drain<void>();
      request.response.headers.contentType = ContentType.json;
      request.response.write(jsonEncode({
        'data': {'host': request.headers.host},
        'request_id': 'r$requests',
      }));
      await request.response.close();
    });
  });

  tearDown(() async {
    await server.close(force: true);
  });

  /// 每次请求用新的 HttpClient，确保真的走一次 connectionFactory（不复用旧连接）。
  Future<String> get(ApiAddressMemory memory, String url) async {
    final client = HttpClient()..connectionFactory = memory.connect;
    try {
      final request = await client.getUrl(Uri.parse(url));
      final response = await request.close();
      return await response.transform(utf8.decoder).join();
    } finally {
      client.close(force: true);
    }
  }

  /// 照常发起连接，同时记下每次连的是哪个目标。
  TcpConnect recording(List<Object> targets) => (host, port) {
        targets.add(host is InternetAddress ? host.address : host);
        return Socket.startConnect(host, port);
      };

  _test('查得到域名时照常按域名连接，并记下查到的地址', () async {
    final targets = <Object>[];
    var lookups = 0;
    final memory = ApiAddressMemory(
      lookup: (host) async {
        lookups++;
        return [InternetAddress.loopbackIPv4];
      },
      connect: recording(targets),
    );
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    expect(lookups, 1);
    expect(targets, ['localhost']);
    expect(memory.remembered('localhost'), [InternetAddress.loopbackIPv4]);
    expect(requests, 1);
  });

  _test('查不到域名时，用上次记下的地址直连', () async {
    final targets = <Object>[];
    var dnsDown = false;
    final memory = ApiAddressMemory(
      lookup: (host) async {
        if (dnsDown) throw const SocketException('Failed host lookup');
        return [InternetAddress.loopbackIPv4];
      },
      connect: recording(targets),
    );
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    dnsDown = true;
    final body = await get(memory, 'http://localhost:${server.port}/v1/ping');
    expect(targets, ['localhost', '127.0.0.1']);
    // 直连 IP 时请求里的 Host 仍是域名。
    expect(jsonDecode(body)['data']['host'], 'localhost');
    expect(requests, 2);
  });

  _test('没记下地址时查不到域名，和原来一样报错', () async {
    final targets = <Object>[];
    final memory = ApiAddressMemory(
      lookup: (host) async => throw const SocketException('Failed host lookup'),
      connect: recording(targets),
    );
    await expectLater(get(memory, 'http://localhost:${server.port}/v1/ping'),
        throwsA(isA<SocketException>()));
    expect(targets, isEmpty);
    expect(requests, 0);
  });

  _test('已记得地址、系统迟迟不答复时，等一会儿就改用记下的地址', () async {
    final targets = <Object>[];
    var calls = 0;
    final never = Completer<List<InternetAddress>>();
    final memory = ApiAddressMemory(
      lookup: (host) {
        calls++;
        return calls == 1
            ? Future.value([InternetAddress.loopbackIPv4])
            : never.future;
      },
      connect: recording(targets),
      lookupGrace: const Duration(milliseconds: 100),
    );
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    final watch = Stopwatch()..start();
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    expect(watch.elapsed, lessThan(const Duration(seconds: 5)));
    expect(targets, ['localhost', '127.0.0.1']);
  });

  _test('记下的地址 IPv4 在前，连不上就换下一个', () async {
    final targets = <Object>[];
    final unreachable = InternetAddress('192.0.2.1');
    final v6 = InternetAddress('2001:db8::1');
    var dnsDown = false;
    final memory = ApiAddressMemory(
      lookup: (host) async {
        if (dnsDown) throw const SocketException('Failed host lookup');
        return [v6, unreachable, InternetAddress.loopbackIPv4];
      },
      connect: (host, port) {
        final target = host is InternetAddress ? host.address : host;
        targets.add(target);
        if (target == unreachable.address || target == v6.address) {
          return Future.value(ConnectionTask.fromSocket(
            Future<Socket>.delayed(const Duration(milliseconds: 1),
                () => throw const SocketException('连不上')),
            () {},
          ));
        }
        return Socket.startConnect(host, port);
      },
    );
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    dnsDown = true;
    targets.clear();
    await get(memory, 'http://localhost:${server.port}/v1/ping');
    expect(targets, ['192.0.2.1', '127.0.0.1']);
    expect(
        ApiAddressMemory.fallbackOrder(
            [v6, unreachable, InternetAddress.loopbackIPv4]),
        [unreachable, InternetAddress.loopbackIPv4, v6]);
  });

  _test('网址本身是 IP 时不查域名', () async {
    var lookups = 0;
    final memory = ApiAddressMemory(lookup: (host) async {
      lookups++;
      return const [];
    });
    await get(memory, 'http://127.0.0.1:${server.port}/v1/ping');
    expect(lookups, 0);
    expect(requests, 1);
  });

  _test('经代理时直接连代理，不查域名', () async {
    final targets = <Object>[];
    var lookups = 0;
    final memory = ApiAddressMemory(
      lookup: (host) async {
        lookups++;
        throw const SocketException('Failed host lookup');
      },
      connect: recording(targets),
    );
    final client = HttpClient()
      ..connectionFactory = memory.connect
      ..findProxy = (_) => 'PROXY 127.0.0.1:${server.port}';
    try {
      final request =
          await client.getUrl(Uri.parse('http://api.example.test/v1/ping'));
      final response = await request.close();
      await response.drain<void>();
      expect(response.statusCode, 200);
    } finally {
      client.close(force: true);
    }
    expect(lookups, 0);
    expect(targets, ['127.0.0.1']);
    expect(requests, 1);
  });

  _test('连接超时时会取消正在进行的连接', () async {
    var cancelled = false;
    final pending = Completer<Socket>();
    final memory = ApiAddressMemory(
      lookup: (host) async => [InternetAddress.loopbackIPv4],
      connect: (host, port) async => ConnectionTask.fromSocket(
        pending.future,
        () {
          cancelled = true;
          pending.completeError(const SocketException('取消'));
        },
      ),
    );
    final client = HttpClient()
      ..connectionFactory = memory.connect
      ..connectionTimeout = const Duration(milliseconds: 200);
    try {
      await expectLater(
          client.getUrl(Uri.parse('http://localhost:${server.port}/v1/ping')),
          throwsA(isA<SocketException>()));
    } finally {
      client.close(force: true);
    }
    expect(cancelled, isTrue);
  });

  _test('ApiClient 传入后，请求经过它连接', () async {
    var lookups = 0;
    final memory = ApiAddressMemory(lookup: (host) async {
      lookups++;
      return [InternetAddress.loopbackIPv4];
    });
    final api = ApiClient(
      baseUrl: 'http://localhost:${server.port}',
      deviceKey: NativeDeviceKey(),
      store: SessionStore(),
      appVersion: 'test',
      platform: 'WINDOWS',
      addressMemory: memory,
    );
    try {
      final data = await api.getPublic('/v1/ping');
      expect(data['host'], 'localhost');
    } finally {
      api.dio.close(force: true);
    }
    expect(lookups, 1);
    expect(memory.remembered('localhost'), [InternetAddress.loopbackIPv4]);
  });
}
