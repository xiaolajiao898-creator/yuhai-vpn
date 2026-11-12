import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/platform/vpn/network_epoch.dart';

void main() {
  test('隧道网卡不计入网络切换', () {
    expect(isVirtualInterface('xray0'), isTrue);
    expect(isVirtualInterface('Wintun'), isTrue);
    expect(isVirtualInterface('yuhai'), isTrue);
    expect(isVirtualInterface('tun0'), isTrue);
    expect(isVirtualInterface('wg0'), isTrue);
    expect(isVirtualInterface('Ethernet'), isFalse);
    expect(isVirtualInterface('wlan0'), isFalse);
    final epoch = NetworkEpoch();
    expect(epoch.observe('Ethernet=192.168.1.9'), 1);
    expect(epoch.observe('Ethernet=192.168.1.9'), 1);
    expect(epoch.observe('wlan0=10.0.0.8'), 2);
  });
}
