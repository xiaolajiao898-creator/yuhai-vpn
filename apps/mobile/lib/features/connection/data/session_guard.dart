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
const tunnelBadLatency = Duration(milliseconds: 800);
const tunnelBadStreak = 2;

const _backoffSeconds = [2, 4, 8, 16, 30];

Duration backoffDelay(int failureIndex) {
  final index = failureIndex < 0 ? 0 : failureIndex;
  final seconds =
      index >= _backoffSeconds.length ? 30 : _backoffSeconds[index];
  return Duration(seconds: seconds);
}

/// 经隧道的一次探测。null 或超过 800ms 视为不健康。
bool tunnelSampleBad(int? latencyMs) =>
    latencyMs == null || latencyMs < 0 || latencyMs >= tunnelBadLatency.inMilliseconds;

/// 国家代码优先。两边都没有国家代码时才比较地区，并且不会跨国家切换。
String? lineArea(String countryCode, String region) {
  final country = countryCode.trim().toUpperCase();
  if (RegExp(r'^[A-Z]{2}$').hasMatch(country)) return 'country:$country';
  final area = region.trim();
  if (area.isNotEmpty) return 'region:$area';
  return null;
}

bool sameLineArea(NodeItem a, NodeItem b) {
  final left = lineArea(a.countryCode, a.region);
  final right = lineArea(b.countryCode, b.region);
  return left != null && left == right;
}

NodeItem? _replacement(NodeItem current, List<NodeItem> lines) {
  final options = lines
      .where((line) =>
          line.id != current.id &&
          line.available &&
          !line.isSaturated &&
          sameLineArea(line, current))
      .toList()
    ..sort((a, b) {
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
  final delay = networkChanged ? backoffDelay(0) : backoffDelay(failuresOnLine - 1);
  if (current == null) {
    return RecoveryDecision(
      action: RecoveryAction.retrySame,
      delay: delay,
      protected: leakProtect,
      notice: leakProtect ? '重连中 · 已保护' : '重连中',
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
  final next = _replacement(current, lines);
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
String protectionNotice(String notice, {required bool wanted, required bool held}) {
  if (!wanted || held) return notice;
  if (notice.contains('已保护')) return notice.replaceAll('已保护', '断网保护没有打开');
  if (notice.isEmpty) return '断网保护没有打开';
  return '$notice · 断网保护没有打开';
}
