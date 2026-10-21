import 'dart:async';
import 'dart:math';

import 'vpn_engine.dart';

/// 阶段一模拟引擎（方案 3.2）：
/// - connect 后随机等待 1.2–2.0 秒进入 connected，5% 概率 HANDSHAKE_TIMEOUT 用于演示失败态
/// - 连接期间每秒产生模拟速率与累计流量
/// - probe 按主机名返回稳定的伪随机延迟
class MockVpnEngine implements VpnEngine {
  MockVpnEngine({Random? random}) : _random = random ?? Random();

  final Random _random;
  final _status = StreamController<VpnStatus>.broadcast();
  final _stats = StreamController<VpnStats>.broadcast();
  final _failures = StreamController<VpnFailure>.broadcast();
  VpnStatus _current = VpnStatus.disconnected;
  Timer? _ticker;
  int _generation = 0;
  bool _disposed = false;
  DateTime? _since;
  int _up = 0, _down = 0;

  @override
  Stream<VpnStatus> get status => _status.stream;
  @override
  Stream<VpnStats> get stats => _stats.stream;
  @override
  Stream<VpnFailure> get failures => _failures.stream;

  @override
  bool get canProbe => !_disposed;

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

  VpnStatus get current => _current;

  void _set(VpnStatus s) {
    if (_disposed) return;
    _current = s;
    _status.add(s);
  }

  @override
  Future<bool> prepare() async => !_disposed;

  @override
  Future<void> connect(ConnectProfile profile) async {
    if (_disposed) return;
    if (profile.protocol != 'MOCK') {
      _generation++;
      _ticker?.cancel();
      _ticker = null;
      _failures.add(const VpnFailure('DEMO_ONLY', '模拟引擎不能建立真实协议连接'));
      _set(VpnStatus.error);
      return;
    }
    if (_current == VpnStatus.connecting || _current == VpnStatus.connected) {
      return;
    }
    final generation = ++_generation;
    _set(VpnStatus.connecting);
    await Future<void>.delayed(
        Duration(milliseconds: 1200 + _random.nextInt(800)));
    if (_disposed ||
        generation != _generation ||
        _current != VpnStatus.connecting) {
      return;
    } // 期间被 disconnect
    if (_random.nextInt(100) < 5) {
      _failures.add(const VpnFailure('HANDSHAKE_TIMEOUT', '握手超时'));
      _set(VpnStatus.error);
      return;
    }
    _since = DateTime.now();
    _up = _down = 0;
    _set(VpnStatus.connected);
    _ticker = Timer.periodic(const Duration(seconds: 1), (_) {
      final upSpeed = 20000 + _random.nextInt(180000);
      final downSpeed = 200000 + _random.nextInt(1800000);
      _up += upSpeed;
      _down += downSpeed;
      _stats.add(VpnStats(
          upBytes: _up,
          downBytes: _down,
          upSpeed: upSpeed,
          downSpeed: downSpeed,
          since: _since!));
    });
  }

  @override
  Future<void> disconnect() async {
    if (_disposed) return;
    final generation = ++_generation;
    _ticker?.cancel();
    _ticker = null;
    if (_current == VpnStatus.disconnected) return;
    _set(VpnStatus.disconnecting);
    await Future<void>.delayed(const Duration(milliseconds: 300));
    if (!_disposed && generation == _generation) _set(VpnStatus.disconnected);
  }

  @override
  Future<int?> probe(String host, int port,
      {Duration timeout = const Duration(seconds: 2)}) async {
    await Future<void>.delayed(const Duration(milliseconds: 120));
    if (_disposed) return null;
    final seed =
        host.codeUnits.fold<int>(0, (a, c) => (a * 31 + c) & 0x7fffffff);
    return 30 + Random(seed).nextInt(220); // 30–250 ms，同一主机稳定
  }

  void dispose() {
    if (_disposed) return;
    _disposed = true;
    _generation++;
    _ticker?.cancel();
    _status.close();
    _stats.close();
    _failures.close();
  }
}
