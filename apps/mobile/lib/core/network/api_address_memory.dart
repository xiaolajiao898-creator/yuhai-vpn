import 'dart:async';
import 'dart:io';

/// 查域名（测试时替换成假的）。
typedef HostLookup = Future<List<InternetAddress>> Function(String host);

/// 发起 TCP 连接（测试时替换成假的）。
typedef TcpConnect = Future<ConnectionTask<Socket>> Function(
    Object host, int port);

/// 记住 API 域名最近一次查到的地址；系统查不到域名时，直接用记下的地址连服务端。
///
/// 起因：Windows 断网保护拦着的时候，系统 DNS 客户端（svchost）也在拦截范围内，
/// 本程序查 API 域名只能靠 Windows 的 DNS 缓存。缓存一过期，重连时取不到新的连接资料，
/// 会一直失败到用户点「断开」。本程序自己发出的连接是放行的，记得上一次的地址就能直连。
///
/// - 平时和原来一样：系统解析域名成功，就记下结果，再照常按域名连接。
/// - 只有系统解析失败，或者已经记得地址、而系统 [lookupGrace] 内还没答复时，才改用记下的地址，
///   IPv4 在前，一个连不上换下一个。
/// - 记下的地址只在内存里：不落盘，也不写死任何 IP。程序退出时断网保护的规则也一起消失。
/// - HTTPS 仍按域名做 TLS：SNI 和证书校验用的都是域名，不是 IP。记下的地址失效或换了主人，
///   握手会失败，不会连到别人的服务器上。
/// - 走代理（例如环境变量 https_proxy）时不经过这里，和原来一样先连代理。
///
/// 用法：`HttpClient()..connectionFactory = memory.connect`。
class ApiAddressMemory {
  ApiAddressMemory({
    HostLookup? lookup,
    TcpConnect? connect,
    this.context,
    this.lookupGrace = const Duration(seconds: 3),
  })  : _lookup = lookup ?? InternetAddress.lookup,
        _tcp = connect ?? Socket.startConnect;

  final HostLookup _lookup;
  final TcpConnect _tcp;

  /// HTTPS 的证书配置；null 表示系统默认，和 HttpClient 默认一致。
  final SecurityContext? context;

  /// 已经记得地址时，最多等系统解析多久。断网保护拦着时，系统可能要等很久才报失败。
  final Duration lookupGrace;

  final _known = <String, List<InternetAddress>>{};

  /// 某个域名当前记下的地址。
  List<InternetAddress> remembered(String host) =>
      _known[host.toLowerCase()] ?? const <InternetAddress>[];

  /// IPv4 在前，其余保持原来的顺序。
  static List<InternetAddress> fallbackOrder(List<InternetAddress> addresses) =>
      [
        ...addresses.where((a) => a.type == InternetAddressType.IPv4),
        ...addresses.where((a) => a.type != InternetAddressType.IPv4),
      ];

  /// 给 [HttpClient.connectionFactory] 用。
  Future<ConnectionTask<Socket>> connect(
      Uri uri, String? proxyHost, int? proxyPort) async {
    if (proxyHost != null && proxyPort != null) {
      // 和 HttpClient 默认的做法一样：先连代理，HTTPS 隧道由 HttpClient 自己建。
      return _tcp(proxyHost, proxyPort);
    }
    final host = uri.host;
    final port = uri.port;
    final tlsHost = uri.isScheme('https') ? host : null;
    if (InternetAddress.tryParse(host) != null) {
      return _open([host], port, tlsHost);
    }
    final key = host.toLowerCase();
    final known = _known[key];
    try {
      final pending = _lookup(host);
      final found =
          known == null ? await pending : await pending.timeout(lookupGrace);
      if (found.isNotEmpty) _known[key] = List.unmodifiable(found);
    } on SocketException {
      if (known == null) rethrow;
      return _open(fallbackOrder(known), port, tlsHost);
    } on TimeoutException {
      return _open(fallbackOrder(known!), port, tlsHost);
    }
    return _open([host], port, tlsHost);
  }

  /// 按顺序连 [targets]，连不上（包括发起就失败）换下一个；TLS 握手失败不换，
  /// 证书对不上说明地址不对。只有一个目标时，报错和 HttpClient 默认的一样。
  /// [tlsHost] 不为 null 时，在连好的 TCP 上按这个域名做 TLS。
  Future<ConnectionTask<Socket>> _open(
      List<Object> targets, int port, String? tlsHost) async {
    var index = 0;
    var cancelled = false;

    Future<ConnectionTask<Socket>> start() async {
      while (true) {
        try {
          return await _tcp(targets[index], port);
        } on SocketException {
          if (cancelled || index + 1 >= targets.length) rethrow;
          index++;
        }
      }
    }

    var current = await start();

    Future<Socket> connected() async {
      while (true) {
        try {
          final socket = await current.socket;
          if (cancelled) {
            socket.destroy();
            throw const SocketException('连接已取消');
          }
          return socket;
        } on SocketException {
          if (cancelled || index + 1 >= targets.length) rethrow;
          index++;
          current = await start();
          if (cancelled) current.cancel();
        }
      }
    }

    Future<Socket> open() async {
      final socket = await connected();
      if (tlsHost == null) return socket;
      final secure =
          await SecureSocket.secure(socket, host: tlsHost, context: context);
      if (cancelled) {
        secure.destroy();
        throw const SocketException('连接已取消');
      }
      return secure;
    }

    return ConnectionTask.fromSocket(open(), () {
      cancelled = true;
      current.cancel();
    });
  }
}
