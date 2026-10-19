import 'vpn_engine.dart';

/// Fail closed until a native implementation and validated profiles are wired.
/// This engine cannot emit a connected state or fabricated network statistics.
class UnavailableVpnEngine implements VpnEngine {
  @override
  bool get canProbe => false;
  @override
  Stream<VpnStatus> get status => const Stream.empty();
  @override
  Stream<VpnStats> get stats => const Stream.empty();
  @override
  Stream<VpnFailure> get failures => const Stream.empty();
  @override
  Future<bool> prepare() async => false;
  @override
  Future<void> connect(ConnectProfile profile) async =>
      throw StateError('Native VPN engine is not configured');
  @override
  Future<void> disconnect() async {}
  @override
  Future<int?> probe(String host, int port,
          {Duration timeout = const Duration(seconds: 2)}) async =>
      null;
  @override
  bool get canRecover => false;
  @override
  Future<void> applyPolicy({required bool leakProtect}) async {}
  @override
  Future<int?> probeTunnel() async => null;
  @override
  Future<bool> holdTraffic() async => false;
  @override
  Future<void> releaseTraffic() async {}
  @override
  Future<int> networkEpoch() async => 0;
}
