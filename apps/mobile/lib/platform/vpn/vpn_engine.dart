/// VPN 引擎抽象（方案 3.2）。UI 只依赖本接口。
enum VpnStatus {
  disconnected,
  connecting,
  connected,
  reconnecting,
  disconnecting,
  error
}

class VpnStats {
  const VpnStats({
    required this.upBytes,
    required this.downBytes,
    required this.upSpeed,
    required this.downSpeed,
    required this.since,
  });
  final int upBytes; // 本次连接累计上行字节
  final int downBytes; // 本次连接累计下行字节
  final int upSpeed; // 字节/秒
  final int downSpeed; // 字节/秒
  final DateTime since; // 连接建立时间
}

class VpnFailure implements Exception {
  const VpnFailure(this.code, this.message);

  /// HANDSHAKE_TIMEOUT / AUTH_REJECTED / PERMISSION_DENIED / NETWORK_UNAVAILABLE / PROFILE_EXPIRED
  final String code;
  final String message;
}

/// 一个节点的一条出站配置，字段与 `POST /v1/connect/profiles` 的 Outbound 对应。
class ConnectProfile {
  const ConnectProfile({
    required this.nodeId,
    required this.protocol,
    required this.server,
    this.extra = const {},
  });
  final String nodeId;
  final String
      protocol; // MOCK / VLESS_REALITY / TROJAN / SHADOWSOCKS_2022 / WIREGUARD
  final String server;
  final Map<String, Object?> extra;
}

abstract interface class VpnEngine {
  bool get canProbe;
  Stream<VpnStatus> get status;
  Stream<VpnStats> get stats;
  Stream<VpnFailure> get failures;

  /// 申请系统 VPN 权限：Android 首次弹出系统授权；iOS 安装 VPN 配置
  Future<bool> prepare();

  Future<void> connect(ConnectProfile profile);
  Future<void> disconnect();

  /// 到节点探测端口的 TCP 连接耗时（毫秒），失败返回 null
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)});
}

/// Native engines keep each account/device's private key inside native storage.
/// Only the corresponding public key may cross the method channel.
abstract interface class WireGuardVpnEngine implements VpnEngine {
  Future<String> publicKey(String identity);
}
