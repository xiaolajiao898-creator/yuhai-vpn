import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/tunnel_host_pins.dart';
import 'package:yuhai_vpn/platform/vpn/windows_wireguard_engine.dart';
import 'package:yuhai_vpn/platform/vpn/windows_xray_config.dart';

import 'egress_profile_test.dart' show egressConfig;
import 'wireguard_profile_test.dart' show parseProfile, profileResponse;

/// Windows 断网保护拦着时系统查不了域名：隧道服务器的域名换成之前查到的 IP。
void main() {
  final v4 = InternetAddress('198.51.100.7');
  final v4b = InternetAddress('198.51.100.8');
  final v6 = InternetAddress('2001:db8::7');

  /// 和服务端生成的形状一样，只是代理出站的服务器地址是域名，并且走 TLS。
  Map<String, dynamic> domainConfig() {
    final config = egressConfig();
    final proxy = (config['outbounds'] as List).first as Map<String, dynamic>;
    proxy['protocol'] = 'trojan';
    proxy['settings'] = {
      'address': 'egress.example.test',
      'port': 443,
      'password': 'TROJANPASSWORD',
    };
    proxy['streamSettings'] = {
      'network': 'raw',
      'security': 'tls',
      'tlsSettings': {'serverName': 'egress.example.test'},
    };
    return config;
  }

  Map<String, dynamic> outbound(Map<String, dynamic> config, String tag) =>
      (config['outbounds'] as List)
          .cast<Map<String, dynamic>>()
          .firstWhere((o) => o['tag'] == tag);

  group('Xray 配置里的服务器域名', () {
    test('只取代理出站的域名，IP 和直连出站不算', () {
      expect(
          xrayProxyHosts(jsonEncode(domainConfig())), ['egress.example.test']);
      expect(xrayProxyHosts(jsonEncode(egressConfig())), isEmpty);
      final old = domainConfig();
      outbound(old, 'proxy')['settings'] = {
        'vnext': [
          {'address': 'a.example.test', 'port': 443},
          {'address': '203.0.113.9', 'port': 443},
        ],
        'servers': [
          {'address': 'b.example.test', 'port': 443},
        ],
      };
      expect(xrayProxyHosts(jsonEncode(old)),
          unorderedEquals(['a.example.test', 'b.example.test']));
    });

    test('写进 dns.hosts，代理出站改用 Xray 自己的 DNS；地址和 SNI 不变，仍通过校验', () {
      final runtime = windowsRuntimeConfig(jsonEncode(domainConfig()));
      final pinned = pinXrayHosts(runtime, {
        'egress.example.test': [v4, v6]
      });
      checkXrayServerConfig(pinned);
      final config = jsonDecode(pinned) as Map<String, dynamic>;
      final dns = config['dns'] as Map<String, dynamic>;
      // 原来的 DNS 设置保留；有 IPv4 时只写 IPv4（queryStrategy 是 UseIPv4）。
      expect(dns['servers'], ['https://1.1.1.1/dns-query']);
      expect(dns['queryStrategy'], 'UseIPv4');
      expect(dns['hosts'], {
        'egress.example.test': ['198.51.100.7']
      });
      final proxy = outbound(config, 'proxy');
      expect(proxy['settings']['address'], 'egress.example.test');
      expect(proxy['streamSettings']['tlsSettings']['serverName'],
          'egress.example.test');
      expect(proxy['streamSettings']['sockopt'], {'domainStrategy': 'UseIP'});
      // 直连出站不动。
      expect(
          outbound(config, 'direct'), {'protocol': 'freedom', 'tag': 'direct'});
      // 凭据和其余部分原样保留。
      expect(pinned, contains('TROJANPASSWORD'));
      expect(config['inbounds'], (jsonDecode(runtime) as Map)['inbounds']);
    });

    test('已有的 hosts 条目和服务端指定的解析方式不覆盖', () {
      final config = domainConfig();
      config['dns']['hosts'] = {'egress.example.test': '203.0.113.50'};
      outbound(config, 'proxy')['streamSettings']
          ['sockopt'] = {'domainStrategy': 'UseIPv4'};
      final pinned = jsonDecode(pinXrayHosts(jsonEncode(config), {
        'egress.example.test': [v4]
      })) as Map<String, dynamic>;
      expect(pinned['dns']['hosts'], {'egress.example.test': '203.0.113.50'});
      expect(outbound(pinned, 'proxy')['streamSettings']['sockopt'],
          {'domainStrategy': 'UseIPv4'});
    });

    test('只有 IPv6 时写 IPv6；没有要换的域名时配置原样返回', () {
      final raw = jsonEncode(domainConfig());
      final pinned = jsonDecode(pinXrayHosts(raw, {
        'egress.example.test': [v6]
      })) as Map<String, dynamic>;
      expect(pinned['dns']['hosts'], {
        'egress.example.test': ['2001:db8::7']
      });
      expect(pinXrayHosts(raw, const {}), raw);
      expect(
          pinXrayHosts(raw, {
            'other.example.test': [v4]
          }),
          raw);
    });
  });

  group('WireGuard endpoint', () {
    test('域名换成 IP，端口不变；IPv6 加方括号', () {
      expect(wireGuardEndpointHost('vpn.example.com:51820'), 'vpn.example.com');
      expect(wireGuardEndpointHost('[2001:db8::1]:51820'), '2001:db8::1');
      expect(wireGuardEndpointHost('vpn.example.com'), isNull);
      expect(pinWireGuardEndpoint('vpn.example.com:51820', v4),
          '198.51.100.7:51820');
      expect(pinWireGuardEndpoint('vpn.example.com:51820', v6),
          '[2001:db8::7]:51820');
      expect(pinWireGuardEndpoint('bad', v4), 'bad');
    });

    test('配置里只接受 IP 字面量的替换，其余照写资料里的 endpoint', () {
      final profile = parseProfile(profileResponse());
      String endpointLine(String? override) =>
          WindowsWireGuardEngine.buildConfig(
                  privateKey: 'KEY', profile: profile, endpoint: override)
              .split('\n')
              .firstWhere((line) => line.startsWith('Endpoint = '));
      expect(endpointLine(null), 'Endpoint = vpn.example.com:51820');
      expect(
          endpointLine('198.51.100.7:51820'), 'Endpoint = 198.51.100.7:51820');
      expect(endpointLine('[2001:db8::7]:51820'),
          'Endpoint = [2001:db8::7]:51820');
      expect(endpointLine('evil.example.com:51820'),
          'Endpoint = vpn.example.com:51820');
    });
  });

  group('TunnelHostMemory', () {
    test('记下查到的地址，IPv4 在前；服务器地址是 IP 时不查', () async {
      final asked = <String>[];
      final memory = TunnelHostMemory(lookup: (host) async {
        asked.add(host);
        return [v6, v4];
      });
      await memory.remember(['vpn.example.com', '203.0.113.9']);
      expect(asked, ['vpn.example.com']);
      expect(memory.remembered('VPN.example.com'), [v4, v6]);
    });

    test('拦着时查得到就不换，查不到才用记下的地址', () async {
      var down = false;
      final memory = TunnelHostMemory(lookup: (host) async {
        if (down) throw const SocketException('Failed host lookup');
        return host == 'a.example.test' ? [v4] : [v4b];
      });
      await memory.remember(['a.example.test']);
      expect(
          await memory.pinsFor(['a.example.test', 'b.example.test']), isEmpty);
      // 上一步查到了 b，也记下了。
      expect(memory.remembered('b.example.test'), [v4b]);
      down = true;
      final pins = await memory.pinsFor(['a.example.test', 'c.example.test']);
      expect(pins, {
        'a.example.test': [v4]
      });
    });

    test('系统迟迟不答复时，等一会儿就用记下的地址', () async {
      var calls = 0;
      final never = Completer<List<InternetAddress>>();
      final memory = TunnelHostMemory(
        lookup: (host) {
          calls++;
          return calls == 1 ? Future.value([v4]) : never.future;
        },
        pinTimeout: const Duration(milliseconds: 50),
      );
      await memory.remember(['vpn.example.com']);
      expect(await memory.pinsFor(['vpn.example.com']), {
        'vpn.example.com': [v4]
      });
    });
  });
}
