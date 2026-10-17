import 'dart:convert';

import 'vpn_engine.dart';

bool validWireGuardPublicKey(Object? value) {
  if (value is! String || value.length != 44) return false;
  try {
    final bytes = base64Decode(value);
    return bytes.length == 32 &&
        bytes.any((byte) => byte != 0) &&
        base64Encode(bytes) == value;
  } on FormatException {
    return false;
  }
}

bool validVpnIdentity(String identity) =>
    RegExp(r'^[A-Za-z0-9_-]{1,64}:[A-Za-z0-9_-]{1,64}$').hasMatch(identity);

bool _ipv4(String value) {
  final parts = value.split('.');
  return parts.length == 4 &&
      parts.every((p) =>
          RegExp(r'^(0|[1-9][0-9]{0,2})$').hasMatch(p) && int.parse(p) <= 255);
}

bool _ip(String value) {
  if (_ipv4(value)) {
    final first = int.parse(value.split('.').first);
    return first > 0 && first < 224;
  }
  // No scopes, mapped IPv4, whitespace, or partial addresses in DNS material.
  if (!RegExp(r'^[0-9A-Fa-f:]{2,39}$').hasMatch(value) ||
      value.contains(':::') ||
      !RegExp(r'[1-9a-fA-F]').hasMatch(value) ||
      value.toLowerCase().startsWith('ff')) {
    return false;
  }
  final pieces = value.split('::');
  if (pieces.length > 2) return false;
  bool groups(String side) =>
      side.isEmpty ||
      side.split(':').every((p) => RegExp(r'^[0-9A-Fa-f]{1,4}$').hasMatch(p));
  if (!pieces.every(groups)) return false;
  final count = pieces
      .where((p) => p.isNotEmpty)
      .fold<int>(0, (n, p) => n + p.split(':').length);
  return pieces.length == 2 ? count < 8 : count == 8;
}

bool validVpnHost(String host) {
  if (_ip(host)) return true;
  if (host.length > 253 || !host.contains('.')) return false;
  // A malformed numeric address must not become a DNS name.
  if (RegExp(r'^[0-9.]+$').hasMatch(host)) return false;
  return host.split('.').every((label) =>
      RegExp(r'^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$')
          .hasMatch(label));
}

bool _endpoint(String value) {
  if (value.length > 320) return false;
  final match = RegExp(r'^(\[[0-9A-Fa-f:]+\]|[^:]+):([1-9][0-9]{0,4})$')
      .firstMatch(value);
  if (match == null) return false;
  var host = match.group(1)!;
  if (host.startsWith('[')) host = host.substring(1, host.length - 1);
  return validVpnHost(host) && int.parse(match.group(2)!) <= 65535;
}

bool _address(String value) {
  if (!value.endsWith('/32')) return false;
  final host = value.substring(0, value.length - 3);
  if (!_ipv4(host)) return false;
  final bytes = host.split('.').map(int.parse).toList();
  return bytes[0] == 10 ||
      (bytes[0] == 172 && bytes[1] >= 16 && bytes[1] <= 31) ||
      (bytes[0] == 192 && bytes[1] == 168);
}

/// Public, validated WireGuard material. A private key is never a Dart value.
class WireGuardProfile extends ConnectProfile {
  WireGuardProfile._({
    required super.nodeId,
    required this.identity,
    required this.endpoint,
    required this.serverPublicKey,
    required this.address,
    required this.dns,
    required this.mtu,
    required this.expiresAt,
  }) : super(protocol: 'WIREGUARD', server: endpoint);

  final String identity, endpoint, serverPublicKey, address;
  final List<String> dns;
  final int mtu;
  final DateTime expiresAt;

  static WireGuardProfile parseResponse(Map<String, dynamic> response,
      {required String nodeId,
      required String identity,
      required String clientPublicKey,
      required DateTime now,
      int clockOffsetSeconds = 0}) {
    const invalid = FormatException('WireGuard 连接资料无效');
    bool exact(Map value, Set<String> keys) =>
        value.length == keys.length && value.keys.every(keys.contains);
    final id = BigInt.tryParse(nodeId);
    if (!validVpnIdentity(identity) ||
        !validWireGuardPublicKey(clientPublicKey) ||
        id == null ||
        id <= BigInt.zero ||
        id > BigInt.parse('9223372036854775807') ||
        id.toString() != nodeId ||
        !exact(response, {'expires_at', 'profiles', 'unavailable'})) {
      throw invalid;
    }
    final rawExpiry = response['expires_at'];
    final expires = rawExpiry is String &&
            RegExp(r'^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$')
                .hasMatch(rawExpiry)
        ? DateTime.tryParse(rawExpiry)
        : null;
    // DateTime.tryParse normalizes dates such as September 31 instead of
    // rejecting them. Network timestamps must name the date they encode.
    if (expires != null &&
        rawExpiry is String &&
        (expires.year != int.parse(rawExpiry.substring(0, 4)) ||
            expires.month != int.parse(rawExpiry.substring(5, 7)) ||
            expires.day != int.parse(rawExpiry.substring(8, 10)) ||
            expires.hour != int.parse(rawExpiry.substring(11, 13)) ||
            expires.minute != int.parse(rawExpiry.substring(14, 16)) ||
            expires.second != int.parse(rawExpiry.substring(17, 19)))) {
      throw invalid;
    }
    final serverNow = now.add(Duration(seconds: clockOffsetSeconds));
    final profiles = response['profiles'];
    final unavailable = response['unavailable'];
    if (expires == null ||
        !expires.isAfter(serverNow) ||
        expires
            .isAfter(serverNow.add(const Duration(minutes: 5, seconds: 5))) ||
        profiles is! List ||
        profiles.length != 1 ||
        unavailable is! List ||
        unavailable.isNotEmpty) {
      throw invalid;
    }
    final profile = profiles.single;
    if (profile is! Map ||
        !exact(profile, {'node_id', 'outbounds'}) ||
        profile['node_id'] != nodeId) {
      throw invalid;
    }
    final outbounds = profile['outbounds'];
    if (outbounds is! List || outbounds.length != 1) throw invalid;
    final out = outbounds.single;
    if (out is! Map ||
        !exact(out, {
          'protocol',
          'endpoint',
          'server_public_key',
          'address',
          'dns',
          'mtu'
        }) ||
        out['protocol'] != 'WIREGUARD' ||
        out['endpoint'] is! String ||
        !_endpoint(out['endpoint']) ||
        !validWireGuardPublicKey(out['server_public_key']) ||
        out['server_public_key'] == clientPublicKey ||
        out['address'] is! String ||
        !_address(out['address']) ||
        out['dns'] is! List ||
        (out['dns'] as List).isEmpty ||
        (out['dns'] as List).length > 8 ||
        (out['dns'] as List).any((v) => v is! String || !_ip(v)) ||
        (out['dns'] as List).toSet().length != (out['dns'] as List).length ||
        out['mtu'] is! int ||
        out['mtu'] < 1280 ||
        out['mtu'] > 1500) {
      throw invalid;
    }
    return WireGuardProfile._(
        nodeId: nodeId,
        identity: identity,
        endpoint: out['endpoint'],
        serverPublicKey: out['server_public_key'],
        address: out['address'],
        dns: List<String>.unmodifiable(out['dns']),
        mtu: out['mtu'],
        expiresAt: expires.subtract(Duration(seconds: clockOffsetSeconds)));
  }

  Map<String, Object> nativeArguments(int generation) => {
        'identity': identity,
        'generation': generation,
        'nodeId': nodeId,
        'protocol': 'WIREGUARD',
        'endpoint': endpoint,
        'serverPublicKey': serverPublicKey,
        'address': address,
        'dns': dns,
        'mtu': mtu,
        'expiresAt': expiresAt.toUtc().toIso8601String(),
      };
}
