import 'dart:convert';

/// Windows 版 Xray TUN 入站要补的固定字段（Xray-core v26.9.9 `infra/conf/tun.go`）。
///
/// 这些值不含凭据：网关地址与安卓 VpnService 使用的前缀相同，DNS 取该 /30 里的
/// 另一个地址，使系统把 53 端口查询送进 TUN，再由服务端配置里的 dns-out 走 DoH。
/// v26.9.9 的 `mtu` 是单个 uint32，不能写成数组；这里只校验、不改写。
const windowsTunGateway = ['172.19.0.1/30', 'fdfe:dcba:9876::1/126'];
const windowsTunDns = ['172.19.0.2'];
const windowsTunRoutes = ['0.0.0.0/0', '::/0'];
const windowsTunOutboundInterface = 'auto';
const windowsTunName = 'xray0';

const _maxConfig = 256 * 1024;
const _topKeys = {
  'log',
  'dns',
  'inbounds',
  'outbounds',
  'routing',
  'stats',
  'policy',
};
const _outboundProtocols = {
  'socks',
  'http',
  'shadowsocks',
  'trojan',
  'vless',
  'vmess',
  'hysteria',
  'freedom',
  'blackhole',
  'dns',
};
final _safeInterfaceName = RegExp(r'^[A-Za-z][A-Za-z0-9_-]{0,31}$');

const _invalid = FormatException('出口连接资料无效');

/// 与安卓 `XrayPolicy.checkConfig` 同一套纵深校验，并要求 `mtu` 为单个正整数。
/// 形状不对就拒绝，绝不改完再拿去用。
void checkXrayServerConfig(String config) {
  if (config.length < 2 || config.length > _maxConfig) throw _invalid;
  if (config.toLowerCase().contains('allowinsecure')) throw _invalid;
  final decoded = _decodeObject(config);
  for (final key in decoded.keys) {
    if (!_topKeys.contains(key)) throw _invalid;
  }
  final log = decoded['log'];
  if (log != null) {
    if (log is! Map) throw _invalid;
    for (final key in log.keys) {
      if (key != 'loglevel') throw _invalid;
    }
  }
  final inbounds = decoded['inbounds'];
  if (inbounds is! List || inbounds.length != 1) throw _invalid;
  final tun = inbounds.single;
  if (tun is! Map || tun['protocol'] != 'tun' || tun.containsKey('listen')) {
    throw _invalid;
  }
  final settings = tun['settings'];
  if (settings != null && settings is! Map) throw _invalid;
  if (settings is Map && settings.containsKey('mtu')) {
    final mtu = settings['mtu'];
    // JSON 数字在 Dart 里要么是 int，要么是 double。数组或小数都不是 v26.9.9 的 uint32。
    if (mtu is! int || mtu <= 0 || mtu > 65535) throw _invalid;
  }
  final name = settings is Map ? settings['name'] : null;
  if (name != null && (name is! String || !_safeInterfaceName.hasMatch(name))) {
    throw _invalid;
  }
  final outbounds = decoded['outbounds'];
  if (outbounds is! List || outbounds.isEmpty || outbounds.length > 8) {
    throw _invalid;
  }
  var proxy = false;
  for (final outbound in outbounds) {
    if (outbound is! Map) throw _invalid;
    if (!_outboundProtocols.contains(outbound['protocol'])) throw _invalid;
    final stream = outbound['streamSettings'];
    if (stream is Map) {
      final sockopt = stream['sockopt'];
      if (sockopt is Map && sockopt.containsKey('dialerProxy')) throw _invalid;
    }
    if (outbound['tag'] == 'proxy') proxy = true;
  }
  if (!proxy) throw _invalid;
  _rejectKey(decoded, 'listen', 0);
}

/// 校验通过后，在内存里的副本上补 Windows TUN 字段。返回值只交给 xray 的标准输入。
String windowsRuntimeConfig(String serverConfig) {
  checkXrayServerConfig(serverConfig);
  final root = _decodeObject(serverConfig);
  final tun = (root['inbounds'] as List).single as Map;
  final settings = tun['settings'];
  final patched = settings is Map
      ? Map<String, Object?>.from(settings)
      : <String, Object?>{};
  patched.putIfAbsent('name', () => windowsTunName);
  patched['gateway'] = windowsTunGateway;
  patched['dns'] = windowsTunDns;
  patched['autoSystemRoutingTable'] = windowsTunRoutes;
  patched['autoOutboundsInterface'] = windowsTunOutboundInterface;
  tun['settings'] = patched;
  final encoded = jsonEncode(root);
  checkXrayServerConfig(encoded);
  return encoded;
}

/// 补丁之后的网卡名，供读取 Wintun 计数。调用前必须已经通过 [windowsRuntimeConfig]。
String windowsRuntimeInterfaceName(String runtimeConfig) {
  final root = _decodeObject(runtimeConfig);
  final tun = (root['inbounds'] as List).single as Map;
  final settings = tun['settings'] as Map;
  final name = settings['name'];
  if (name is! String || !_safeInterfaceName.hasMatch(name)) throw _invalid;
  return name;
}

Map<String, dynamic> _decodeObject(String config) {
  try {
    final decoded = jsonDecode(config);
    if (decoded is Map<String, dynamic>) return decoded;
    if (decoded is Map) return Map<String, dynamic>.from(decoded);
  } on FormatException {
    throw _invalid;
  }
  throw _invalid;
}

void _rejectKey(Object? value, String key, int depth) {
  if (depth >= 16) throw _invalid;
  if (value is Map) {
    for (final entry in value.entries) {
      if (entry.key == key) throw _invalid;
      _rejectKey(entry.value, key, depth + 1);
    }
  } else if (value is List) {
    for (final item in value) {
      _rejectKey(item, key, depth + 1);
    }
  }
}
