import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../app/providers.dart';
import '../../../core/network/api_client.dart';
import '../../../platform/vpn/egress_profile.dart';

class NodeItem {
  const NodeItem(
      {required this.id,
      required this.name,
      required this.countryCode,
      required this.city,
      required this.region,
      required this.available,
      this.lockedReason,
      this.load,
      required this.isRecommended,
      this.probeHost,
      this.probePort,
      required this.protocols,
      this.supportsUdp,
      this.onlineCount,
      this.capacityUsers,
      this.saturated});
  final String id, name, countryCode, city, region;
  final bool available, isRecommended;
  final String? lockedReason, probeHost;
  final int? load, probePort;
  final List<String> protocols;

  /// 仅出口有此信息：该出口是否支持 UDP（HTTP/HTTPS 代理恒为否）。WireGuard 节点为 null。
  final bool? supportsUdp;

  /// 节点心跳上报的在线数和容量。出口没有心跳，容量为 0 表示未设上限。
  final int? onlineCount;
  final int? capacityUsers;
  final bool? saturated;

  bool get isSaturated {
    if (saturated == true) return true;
    final cap = capacityUsers;
    final online = onlineCount;
    if (cap == null || online == null) return false;
    return cap > 0 && online >= cap;
  }

  /// 出口（Xray 内核）编号以 e 开头，与 WireGuard 节点的纯数字编号区分，客户端据此选择引擎。
  bool get isEgress => id.startsWith('e');

  String get unavailableLabel => switch (lockedReason) {
        'TIER' => '需要更高等级套餐',
        'MAINTENANCE' => '维护中',
        'OFFLINE' => '节点暂时离线',
        'NOT_READY' => '节点准备中',
        'PROTOCOL_UNSUPPORTED' => '当前版本不支持该线路协议',
        'PLATFORM' => '当前设备暂不支持该线路类型',
        _ => '节点暂不可用',
      };

  /// `GET /v1/egress` 的一条。出口不公开主机与端口，因此没有探测地址，也不参与自动选择。
  static NodeItem fromEgressJson(Map<String, dynamic> j,
      {required bool platformSupported}) {
    bool text(dynamic v, int limit, {bool allowEmpty = false}) =>
        v is String &&
        (allowEmpty || v.isNotEmpty) &&
        v.length <= limit &&
        !RegExp(r'[\x00-\x1f]').hasMatch(v);
    const keys = {
      'id',
      'name',
      'country_code',
      'city',
      'region',
      'protocol',
      'supports_udp',
      'capacity_users',
      'available',
      'locked_reason'
    };
    final protocol = j['protocol'];
    if (j.length != keys.length ||
        !j.keys.every(keys.contains) ||
        !validEgressId(j['id']) ||
        !text(j['name'], 64) ||
        j['country_code'] is! String ||
        !RegExp(r'^([A-Z]{2})?$').hasMatch(j['country_code']) ||
        !text(j['city'], 64, allowEmpty: true) ||
        !text(j['region'], 32, allowEmpty: true) ||
        protocol is! String ||
        protocol.length > 32 ||
        j['supports_udp'] is! bool ||
        j['capacity_users'] is! int ||
        j['capacity_users'] < 0 ||
        j['capacity_users'] > 1000000 ||
        j['available'] is! bool ||
        (j['locked_reason'] != null && !text(j['locked_reason'], 32)) ||
        (j['available'] == true && j['locked_reason'] != null) ||
        (j['available'] == false && j['locked_reason'] == null) ||
        (j['available'] == true && !egressProtocols.contains(protocol))) {
      throw const FormatException('线路资料无效');
    }
    final available = j['available'] == true && platformSupported;
    return NodeItem(
        id: j['id'],
        name: j['name'],
        countryCode: j['country_code'],
        city: j['city'],
        region: j['region'],
        available: available,
        lockedReason: available
            ? null
            : (j['available'] == true ? 'PLATFORM' : j['locked_reason']),
        isRecommended: false,
        protocols: available ? List<String>.unmodifiable([protocol]) : const [],
        supportsUdp: j['supports_udp'],
        capacityUsers: j['capacity_users'] as int);
  }

  static NodeItem fromJson(Map<String, dynamic> j, {bool allowMock = false}) {
    bool text(dynamic v, int limit) =>
        v is String &&
        v.isNotEmpty &&
        v.length <= limit &&
        !RegExp(r'[\x00-\x1f]').hasMatch(v);
    final id = j['id'];
    final number = id is String ? BigInt.tryParse(id) : null;
    if (!text(id, 64) ||
        (!allowMock &&
            (number == null ||
                number <= BigInt.zero ||
                number > BigInt.parse('9223372036854775807') ||
                number.toString() != id)) ||
        !text(j['name'], 64) ||
        j['country_code'] is! String ||
        !RegExp(r'^[A-Z]{2}$').hasMatch(j['country_code']) ||
        !text(j['region'], 32) ||
        j['available'] is! bool ||
        j['is_recommended'] is! bool ||
        j['protocols'] is! List ||
        (j['city'] != null &&
            (j['city'] is! String || (j['city'] as String).length > 64))) {
      throw const FormatException('节点资料无效');
    }
    final available = j['available'] as bool;
    final protocols = j['protocols'] as List;
    const real = {
      'VLESS_REALITY',
      'TROJAN',
      'SHADOWSOCKS_2022',
      'WIREGUARD',
      'HYSTERIA2'
    };
    if (protocols.length > 64 ||
        protocols.toSet().length != protocols.length ||
        protocols.any((p) =>
            p is! String ||
            (!real.contains(p) && !(allowMock && p == 'MOCK')))) {
      throw const FormatException('节点协议无效');
    }
    final reason = j['locked_reason'];
    final load = j['load'];
    final probe = j['probe'];
    int? count(dynamic value) {
      if (value == null) return null;
      if (value is! int || value < 0 || value > 1000000) {
        throw const FormatException('节点负载资料无效');
      }
      return value;
    }

    final online = count(j['online_count']);
    final capacity = count(j['capacity_users']);
    final saturatedFlag = j['saturated'];
    if (saturatedFlag != null && saturatedFlag is! bool) {
      throw const FormatException('节点负载资料无效');
    }
    if ((reason != null && !text(reason, 32)) ||
        (load != null && (load is! int || load < 0 || load > 100)) ||
        (available &&
            (reason != null ||
                load == null ||
                protocols.isEmpty ||
                probe is! Map ||
                !text(probe['host'], 255) ||
                !RegExp(r'^[A-Za-z0-9:._-]+$').hasMatch(probe['host']) ||
                probe['port'] is! int ||
                probe['port'] < 1 ||
                probe['port'] > 65535)) ||
        (!available &&
            (probe != null || load != null || protocols.isNotEmpty))) {
      throw const FormatException('节点可用状态不一致');
    }
    return NodeItem(
        id: id,
        name: j['name'],
        countryCode: j['country_code'],
        city: j['city'] ?? '',
        region: j['region'],
        available: available,
        lockedReason: reason,
        load: load,
        isRecommended: j['is_recommended'],
        probeHost: available ? probe['host'] : null,
        probePort: available ? probe['port'] : null,
        protocols: List<String>.unmodifiable(protocols),
        onlineCount: available ? online : null,
        capacityUsers: available ? capacity : null,
        saturated: available ? saturatedFlag as bool? : null);
  }
}

/// Only the isolated preview entry overrides this; real API never accepts MOCK.
final nodeDemoModeProvider = Provider<bool>((_) => false);

List<NodeItem> parseNodes(Map<String, dynamic> data, {bool allowMock = false}) {
  final raw = data['nodes'];
  if (raw is! List || raw.length > 1000) throw const FormatException('节点列表不完整');
  final ids = <String>{};
  final result = <NodeItem>[];
  for (final value in raw) {
    if (value is! Map<String, dynamic>) throw const FormatException('节点资料无效');
    final node = NodeItem.fromJson(value, allowMock: allowMock);
    if (!ids.add(node.id)) throw const FormatException('节点编号重复');
    result.add(node);
  }
  return List<NodeItem>.unmodifiable(result);
}

List<NodeItem> parseEgress(Map<String, dynamic> data,
    {required bool platformSupported}) {
  final raw = data['egress'];
  if (raw is! List || raw.length > 1000) throw const FormatException('线路列表不完整');
  final ids = <String>{};
  final result = <NodeItem>[];
  for (final value in raw) {
    if (value is! Map<String, dynamic>) throw const FormatException('线路资料无效');
    final item =
        NodeItem.fromEgressJson(value, platformSupported: platformSupported);
    if (!ids.add(item.id)) throw const FormatException('线路编号重复');
    result.add(item);
  }
  return result;
}

/// 出口可由安卓与 Windows 的 Xray 内核连接；其他平台照样列出但标明不支持，不静默隐藏。
bool get egressPlatformSupported =>
    !kIsWeb &&
    (defaultTargetPlatform == TargetPlatform.android ||
        defaultTargetPlatform == TargetPlatform.windows);

/// Each refresh reads a complete server snapshot; failures never become empty success.
/// 出口列表是后加的接口：只有「服务端尚未提供该接口」（40401）时视为没有出口，
/// 其余错误照常抛出，不把失败伪装成空列表。
final nodesProvider = FutureProvider.autoDispose<List<NodeItem>>((ref) async {
  final api = ref.watch(apiClientProvider);
  final demo = ref.watch(nodeDemoModeProvider);
  final d = await api.call('GET', '/v1/nodes');
  final nodes = parseNodes(d, allowMock: demo);
  if (demo) return nodes;
  List<NodeItem> egress;
  try {
    egress = parseEgress(await api.call('GET', '/v1/egress'),
        platformSupported: egressPlatformSupported);
  } on ApiException catch (e) {
    if (e.code != 40401) rethrow;
    egress = const [];
  }
  return List<NodeItem>.unmodifiable([...nodes, ...egress]);
});
