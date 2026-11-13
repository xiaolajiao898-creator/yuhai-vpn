import 'dart:io';

/// 隧道网卡不算「网络切换」。名字随驱动变化，所以按子串排除。
bool isVirtualInterface(String name) {
  final n = name.toLowerCase();
  return n.contains('xray') ||
      n.contains('wintun') ||
      n.contains('yuhai') ||
      n.contains('wireguard') ||
      n == 'tun' ||
      n.startsWith('tun') ||
      n.startsWith('wg');
}

String physicalInterfaceSignature(Iterable<NetworkInterface> interfaces) {
  final rows = <String>[];
  for (final iface in interfaces) {
    if (isVirtualInterface(iface.name)) continue;
    final addresses = iface.addresses.map((address) => address.address).toList()
      ..sort();
    rows.add('${iface.name}=${addresses.join(',')}');
  }
  rows.sort();
  return rows.join('|');
}

/// 签名变化才加一。第一次看到签名也算一次变化，调用方应先记下来再比较。
class NetworkEpoch {
  String? _signature;
  int _epoch = 0;

  int observe(String signature) {
    if (signature != _signature) {
      _signature = signature;
      _epoch++;
    }
    return _epoch;
  }
}
