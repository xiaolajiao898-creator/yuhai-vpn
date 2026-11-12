import '../../nodes/data/nodes_api.dart';

/// 自动重连和同地区切换共用这一份决定，避免两套策略抢同一条隧道。
enum RecoveryAction { release, hold, retrySame, switchLine }

class RecoveryDecision {
  const RecoveryDecision({
    required this.action,
    required this.delay,
    required this.notice,
    required this.protected,
    this.lineId,
  });

  final RecoveryAction action;
  final String? lineId;
  final Duration delay;
  final String notice;

  /// true 表示这次重连期间应拦住本地网络。是否真的拦住，由引擎回执决定。
  final bool protected;
}

const tunnelProbeInterval = Duration(seconds: 20);

/// 慢不等于断。探测是一次完整的 HTTPS 请求（含 DNS、TCP、TLS），跨洲线路本来就要
/// 七八百毫秒；拿 800ms 当门槛会让远端线路每 40 秒被判坏一次、反复重建（批发线路每次
/// 重连都要在 WorldVPN 上删一条、加一条 peer）。只有探测失败或慢到接近超时才算不健康。
const tunnelBadLatency = Duration(seconds: 5);
const tunnelBadStreak = 2;

const _backoffSeconds = [2, 4, 8, 16, 30];

Duration backoffDelay(int failureIndex) {
  final index = failureIndex < 0 ? 0 : failureIndex;
  final seconds = index >= _backoffSeconds.length ? 30 : _backoffSeconds[index];
  return Duration(seconds: seconds);
}

/// 经隧道的一次探测。null（失败）或达到 [tunnelBadLatency] 视为不健康。
bool tunnelSampleBad(int? latencyMs) =>
    latencyMs == null ||
    latencyMs < 0 ||
    latencyMs >= tunnelBadLatency.inMilliseconds;

/// 「未知」占位：WorldVPN 认不出地名的线路是 ZZ / OTHER，不能当成同一个国家互相切换。
const _unknownCountries = {'ZZ', 'XX'};
const _unknownRegions = {'OTHER'};

/// 国家代码优先。两边都没有国家代码时才比较地区，并且不会跨国家切换。
/// 国家或地区未知（ZZ、OTHER）时返回 null，调用方只会重试当前线路。
String? lineArea(String countryCode, String region) {
  final country = countryCode.trim().toUpperCase();
  if (RegExp(r'^[A-Z]{2}$').hasMatch(country) &&
      !_unknownCountries.contains(country)) {
    return 'country:$country';
  }
  if (country.isNotEmpty) return null;
  final area = region.trim();
  if (area.isNotEmpty && !_unknownRegions.contains(area.toUpperCase())) {
    return 'region:$area';
  }
  return null;
}

/// 线路种类：w 开头是 WorldVPN 批发，e 开头是出口（Xray），纯数字是自建 WireGuard 节点。
String lineFamily(String id) {
  if (id.startsWith('w')) return 'wholesale';
  if (id.startsWith('e')) return 'egress';
  return 'node';
}

bool sameLineArea(NodeItem a, NodeItem b) {
  final left = lineArea(a.countryCode, a.region);
  final right = lineArea(b.countryCode, b.region);
  return left != null && left == right;
}

/// 同一国家/地区里挑下一条：先挑这轮还没失败过的，再挑同种线路（批发换批发、出口换出口，
/// Windows 上不必在 Xray 和 WireGuard 之间来回切），然后是推荐的，最后按名字和编号。
/// [avoid] 里都是这轮已经换走过的线路；全部试过一遍后才回头。
NodeItem? _replacement(
    NodeItem current, List<NodeItem> lines, Set<String> avoid) {
  final family = lineFamily(current.id);
  final options = lines
      .where((line) =>
          line.id != current.id &&
          line.available &&
          !line.isSaturated &&
          sameLineArea(line, current))
      .toList()
    ..sort((a, b) {
      final aTried = avoid.contains(a.id), bTried = avoid.contains(b.id);
      if (aTried != bTried) return aTried ? 1 : -1;
      final aSame = lineFamily(a.id) == family, bSame = lineFamily(b.id) == family;
      if (aSame != bSame) return aSame ? -1 : 1;
      if (a.isRecommended != b.isRecommended) return a.isRecommended ? -1 : 1;
      final byName = a.name.compareTo(b.name);
      return byName != 0 ? byName : a.id.compareTo(b.id);
    });
  return options.firstOrNull;
}

/// [failuresOnLine] 从 1 开始。前两次留在当前线路，之后才在同一国家/地区里换。
/// 网络变化会先重试当前线路。没有同一地区的可用线路时不跨地区。
RecoveryDecision decideRecovery({
  required bool leakProtect,
  required bool autoRecover,
  required bool networkChanged,
  required int failuresOnLine,
  required NodeItem? current,
  required List<NodeItem> lines,
  Set<String> avoid = const {},
}) {
  if (!autoRecover) {
    if (leakProtect) {
      return const RecoveryDecision(
        action: RecoveryAction.hold,
        delay: Duration.zero,
        protected: true,
        notice: '已保护。流量已拦住，点「断开」才会恢复本地网络。',
      );
    }
    return const RecoveryDecision(
      action: RecoveryAction.release,
      delay: Duration.zero,
      protected: false,
      notice: '',
    );
  }
  final delay =
      networkChanged ? backoffDelay(0) : backoffDelay(failuresOnLine - 1);
  if (current == null) {
    return RecoveryDecision(
      action: RecoveryAction.retrySame,
      delay: delay,
      protected: leakProtect,
      notice: networkChanged
          ? (leakProtect ? '网络已切换，重连中 · 已保护' : '网络已切换，重连中')
          : (leakProtect ? '重连中 · 已保护' : '重连中'),
    );
  }
  final stay = RecoveryDecision(
    action: RecoveryAction.retrySame,
    lineId: current.id,
    delay: delay,
    protected: leakProtect,
    notice: networkChanged
        ? (leakProtect ? '网络已切换，重连中 · 已保护' : '网络已切换，重连中')
        : (leakProtect ? '重连中 · 已保护' : '重连中'),
  );
  if (networkChanged || failuresOnLine < 3) return stay;
  if (lineArea(current.countryCode, current.region) == null) {
    return RecoveryDecision(
      action: RecoveryAction.retrySame,
      lineId: current.id,
      delay: delay,
      protected: leakProtect,
      notice: '无法判断线路所在地区，不会跨地区切换。重连中。',
    );
  }
  final next = _replacement(current, lines, avoid);
  if (next == null) {
    return RecoveryDecision(
      action: RecoveryAction.retrySame,
      lineId: current.id,
      delay: delay,
      protected: leakProtect,
      notice: '同一地区没有其他可用线路，不会跨地区切换。重连中。',
    );
  }
  return RecoveryDecision(
    action: RecoveryAction.switchLine,
    lineId: next.id,
    delay: delay,
    protected: leakProtect,
    notice: '已切换线路 · ${next.name}${leakProtect ? ' · 已保护' : ''}',
  );
}

/// 引擎没有真正拦住流量时，不能对用户说「已保护」。
String protectionNotice(String notice,
    {required bool wanted, required bool held}) {
  if (!wanted || held) return notice;
  if (notice.contains('已保护')) return notice.replaceAll('已保护', '断网保护没有打开');
  if (notice.isEmpty) return '断网保护没有打开';
  return '$notice · 断网保护没有打开';
}
