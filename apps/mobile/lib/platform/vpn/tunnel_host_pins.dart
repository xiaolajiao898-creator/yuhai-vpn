import 'dart:async';
import 'dart:convert';
import 'dart:io';

/// 查域名（测试时替换成假的）。
typedef TunnelHostLookup = Future<List<InternetAddress>> Function(String host);

/// 记住隧道服务器域名最近一次查到的地址，供 Windows 断网保护拦着时使用。
///
/// 起因：断网保护拦着的时候，系统 DNS 客户端（svchost）也在拦截范围内。xray.exe、wireguard.exe
/// 解析服务器域名只能靠 Windows 的 DNS 缓存，缓存过期后新隧道起不来。本程序、xray.exe 和
/// wireguard.exe 自己发出的连接是放行的，所以只要知道 IP，就能直接连上服务器。
///
/// - 平时（没拦着）连接时，顺手把服务器域名查一遍记下来，不改任何连接资料。
/// - 拦着的时候先照常查：查得到就不替换；查不到（或 [pinTimeout] 内没答复）而记得地址时，
///   才把记下的地址交给 [pinXrayHosts] / [pinWireGuardEndpoint]。
/// - 只在内存里，不落盘，不写死任何 IP；服务器地址本来就是 IP 时什么也不做。
class TunnelHostMemory {
  TunnelHostMemory({
    TunnelHostLookup? lookup,
    this.rememberTimeout = const Duration(seconds: 3),
    this.pinTimeout = const Duration(seconds: 2),
  }) : _lookup = lookup ?? InternetAddress.lookup;

  final TunnelHostLookup _lookup;
  final Duration rememberTimeout;
  final Duration pinTimeout;
  final _known = <String, List<InternetAddress>>{};

  /// 某个域名当前记下的地址，IPv4 在前。
  List<InternetAddress> remembered(String host) =>
      _known[host.toLowerCase()] ?? const <InternetAddress>[];

  /// 查一遍这些域名并记下结果。查不到的保留原来记下的，不报错。
  Future<void> remember(Iterable<String> hosts) async {
    for (final host in _domains(hosts)) {
      try {
        _store(host, await _lookup(host).timeout(rememberTimeout));
      } catch (_) {}
    }
  }

  /// 断网保护拦着时调用：返回需要换成 IP 的域名和它们记下的地址。
  /// 查得到的域名不放进结果（顺便更新记下的地址），查不到又没记过的也不放。
  Future<Map<String, List<InternetAddress>>> pinsFor(
      Iterable<String> hosts) async {
    final pins = <String, List<InternetAddress>>{};
    for (final host in _domains(hosts)) {
      try {
        final found = await _lookup(host).timeout(pinTimeout);
        if (found.isNotEmpty) {
          _store(host, found);
          continue;
        }
      } catch (_) {}
      final known = remembered(host);
      if (known.isNotEmpty) pins[host] = known;
    }
    return pins;
  }

  void _store(String host, List<InternetAddress> found) {
    if (found.isEmpty) return;
    _known[host.toLowerCase()] = List.unmodifiable([
      ...found.where((a) => a.type == InternetAddressType.IPv4),
      ...found.where((a) => a.type == InternetAddressType.IPv6),
    ]);
  }

  static Iterable<String> _domains(Iterable<String> hosts) => hosts
      .where((h) => h.isNotEmpty && InternetAddress.tryParse(h) == null)
      .toSet();
}

/// 不是代理的出站（直连、丢弃、DNS）不连服务器。
const _localOutbounds = {'freedom', 'blackhole', 'dns'};

/// Xray 配置里代理出站要连的服务器域名（是 IP 的不算）。
///
/// 服务端生成的是扁平写法 `settings.address`；也认 `settings.vnext[].address` 和
/// `settings.servers[].address` 两种老写法。
List<String> xrayProxyHosts(String config) {
  final root = jsonDecode(config);
  if (root is! Map) return const [];
  final hosts = <String>{};
  for (final outbound in _proxyOutbounds(root)) {
    for (final address in _serverAddresses(outbound)) {
      if (InternetAddress.tryParse(address) == null) hosts.add(address);
    }
  }
  return hosts.toList();
}

/// 把 [pins] 里的域名写进 Xray 的 `dns.hosts`，并让连这些域名的代理出站用 Xray 自己的 DNS
/// 解析（`streamSettings.sockopt.domainStrategy`），这样 xray.exe 不经系统 DNS 也能拿到 IP。
///
/// 出站的 `address` 和 TLS 的 `serverName` 都不改：TLS 仍按域名做 SNI 和证书校验。
/// 已有的 `dns.hosts` 条目和服务端已经指定的 `domainStrategy` 不覆盖。
/// Xray 的 DNS 设了 `queryStrategy: UseIPv4` 时只用 IPv4，所以有 IPv4 时只写 IPv4。
String pinXrayHosts(String config, Map<String, List<InternetAddress>> pins) {
  if (pins.isEmpty) return config;
  final root = jsonDecode(config);
  if (root is! Map<String, dynamic>) throw const FormatException('出口连接资料无效');
  final wanted = {for (final e in pins.entries) e.key.toLowerCase(): e.value};
  final used = <String, List<InternetAddress>>{};
  for (final outbound in _proxyOutbounds(root)) {
    final hits = _serverAddresses(outbound)
        .where((a) => wanted.containsKey(a.toLowerCase()))
        .toList();
    if (hits.isEmpty) continue;
    for (final hit in hits) {
      used[hit] = wanted[hit.toLowerCase()]!;
    }
    final stream = outbound['streamSettings'];
    final streamMap = stream is Map ? stream : <String, dynamic>{};
    outbound['streamSettings'] = streamMap;
    final sockopt = streamMap['sockopt'];
    final sockMap = sockopt is Map ? sockopt : <String, dynamic>{};
    streamMap['sockopt'] = sockMap;
    final strategy = sockMap['domainStrategy'];
    if (strategy == null || strategy == 'AsIs') {
      sockMap['domainStrategy'] = 'UseIP';
    }
  }
  if (used.isEmpty) return config;
  final dns = root['dns'];
  final dnsMap = dns is Map ? dns : <String, dynamic>{};
  root['dns'] = dnsMap;
  final hosts = dnsMap['hosts'];
  final hostsMap = hosts is Map ? hosts : <String, dynamic>{};
  dnsMap['hosts'] = hostsMap;
  for (final entry in used.entries) {
    if (hostsMap.containsKey(entry.key)) continue;
    final v4 = entry.value
        .where((a) => a.type == InternetAddressType.IPv4)
        .map((a) => a.address)
        .toList();
    hostsMap[entry.key] =
        v4.isNotEmpty ? v4 : entry.value.map((a) => a.address).toList();
  }
  return jsonEncode(root);
}

/// WireGuard 的 `host:port` 里的主机部分（IPv6 去掉方括号）；格式不对返回 null。
String? wireGuardEndpointHost(String endpoint) {
  final match = RegExp(r'^(\[[0-9A-Fa-f:]+\]|[^:\[\]]+):([0-9]{1,5})$')
      .firstMatch(endpoint);
  if (match == null) return null;
  final host = match.group(1)!;
  return host.startsWith('[') ? host.substring(1, host.length - 1) : host;
}

/// 把 `域名:端口` 换成 `IP:端口`（IPv6 加方括号），端口不变。格式不对时原样返回。
String pinWireGuardEndpoint(String endpoint, InternetAddress address) {
  final match = RegExp(r':([0-9]{1,5})$').firstMatch(endpoint);
  if (match == null || wireGuardEndpointHost(endpoint) == null) return endpoint;
  final port = match.group(1)!;
  return address.type == InternetAddressType.IPv6
      ? '[${address.address}]:$port'
      : '${address.address}:$port';
}

Iterable<Map> _proxyOutbounds(Map root) sync* {
  final outbounds = root['outbounds'];
  if (outbounds is! List) return;
  for (final outbound in outbounds) {
    if (outbound is Map && !_localOutbounds.contains(outbound['protocol'])) {
      yield outbound;
    }
  }
}

Iterable<String> _serverAddresses(Map outbound) sync* {
  final settings = outbound['settings'];
  if (settings is! Map) return;
  final flat = settings['address'];
  if (flat is String) yield flat;
  for (final key in const ['vnext', 'servers']) {
    final list = settings[key];
    if (list is! List) continue;
    for (final server in list) {
      if (server is Map && server['address'] is String) {
        yield server['address'] as String;
      }
    }
  }
}
