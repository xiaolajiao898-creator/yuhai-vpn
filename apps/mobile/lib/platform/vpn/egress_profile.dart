import 'dart:convert';

import 'vpn_engine.dart';
import 'wireguard_profile.dart';

/// 出口（外购代理 / 自建多协议节点）的协议编号，与服务端 egress.Supported 一致。
const egressProtocols = {
  'SOCKS5',
  'HTTP',
  'HTTPS',
  'SHADOWSOCKS',
  'TROJAN',
  'VLESS',
  'VMESS',
  'HYSTERIA2',
};

/// 界面上的协议名称。
String egressProtocolLabel(String protocol) => switch (protocol) {
      'SOCKS5' => 'SOCKS5',
      'HTTP' => 'HTTP 代理',
      'HTTPS' => 'HTTPS 代理',
      'SHADOWSOCKS' => 'Shadowsocks',
      'TROJAN' => 'Trojan',
      'VLESS' => 'VLESS',
      'VMESS' => 'VMess',
      'HYSTERIA2' => 'Hysteria2',
      'WIREGUARD' => 'WireGuard',
      _ => protocol,
    };

bool validEgressId(Object? id) =>
    id is String && RegExp(r'^e[1-9][0-9]{0,18}$').hasMatch(id);

/// `POST /v1/connect/egress` 的结果。config 是服务端生成、交给 Xray 内核原样使用的完整配置，
/// 含出口凭据：只进内存、只经方法通道交给原生层，不写日志、不落盘。
class EgressProfile extends ConnectProfile {
  EgressProfile._({
    required super.nodeId,
    required super.protocol,
    required this.identity,
    required this.supportsUdp,
    required this.config,
    required this.expiresAt,
    required this.notice,
  }) : super(server: '');

  final String identity;
  final bool supportsUdp;
  final String config;
  final DateTime expiresAt;

  /// 服务端给出的如实提示（例如该出口不支持 UDP），为空表示无。
  final String notice;

  static const _maxConfig = 256 * 1024;

  static EgressProfile parseResponse(Map<String, dynamic> response,
      {required String egressId,
      required String identity,
      required DateTime now,
      int clockOffsetSeconds = 0}) {
    const invalid = FormatException('出口连接资料无效');
    const keys = {
      'egress_id',
      'protocol',
      'supports_udp',
      'core',
      'core_version',
      'expires_at',
      'config',
      'notice'
    };
    if (!validEgressId(egressId) ||
        !validVpnIdentity(identity) ||
        response.length != keys.length ||
        !response.keys.every(keys.contains) ||
        response['egress_id'] != egressId ||
        response['core'] != 'xray' ||
        response['core_version'] is! String ||
        !egressProtocols.contains(response['protocol']) ||
        response['supports_udp'] is! bool ||
        response['notice'] is! String ||
        (response['notice'] as String).length > 200 ||
        response['config'] is! Map) {
      throw invalid;
    }
    // HTTP/HTTPS 代理没有 UDP：服务端若声称支持，说明资料被篡改或不一致，拒绝而不是照用。
    if ((response['protocol'] == 'HTTP' || response['protocol'] == 'HTTPS') &&
        response['supports_udp'] == true) {
      throw invalid;
    }
    final rawExpiry = response['expires_at'];
    final expires = rawExpiry is String &&
            RegExp(r'^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$')
                .hasMatch(rawExpiry)
        ? DateTime.tryParse(rawExpiry)
        : null;
    final serverNow = now.add(Duration(seconds: clockOffsetSeconds));
    if (expires == null ||
        !expires.isAfter(serverNow) ||
        expires.isAfter(serverNow.add(const Duration(days: 400)))) {
      throw invalid;
    }
    final config = jsonEncode(response['config']);
    final cfg = response['config'] as Map;
    final inbounds = cfg['inbounds'];
    if (config.length > _maxConfig ||
        config.toLowerCase().contains('allowinsecure') ||
        inbounds is! List ||
        inbounds.length != 1 ||
        inbounds.single is! Map ||
        (inbounds.single as Map)['protocol'] != 'tun' ||
        (inbounds.single as Map).containsKey('listen') ||
        cfg['outbounds'] is! List ||
        !(cfg['outbounds'] as List)
            .any((o) => o is Map && o['tag'] == 'proxy')) {
      throw invalid;
    }
    return EgressProfile._(
        nodeId: egressId,
        protocol: response['protocol'],
        identity: identity,
        supportsUdp: response['supports_udp'],
        config: config,
        expiresAt: expires.subtract(Duration(seconds: clockOffsetSeconds)),
        notice: response['notice']);
  }

  Map<String, Object> nativeArguments(int generation) => {
        'identity': identity,
        'generation': generation,
        'egressId': nodeId,
        'protocol': protocol,
        'config': config,
        'expiresAt': expiresAt.toUtc().toIso8601String(),
      };
}
