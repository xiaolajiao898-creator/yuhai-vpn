/// 本地构建号和线上发布比一比，决定要不要升级。
enum UpdateKind { current, optional, forced }

class AppReleaseInfo {
  const AppReleaseInfo({
    required this.version,
    required this.build,
    required this.url,
    required this.sha256,
    required this.size,
    required this.releasedAt,
    required this.minSupportedBuild,
    required this.notes,
    this.certSha256,
  });

  final String version;
  final int build;
  final String url;
  final String sha256;
  final int size;
  final DateTime releasedAt;
  final int minSupportedBuild;
  final String notes;
  final String? certSha256;

  static AppReleaseInfo? parse(Object? body) {
    final root = body is Map ? body : null;
    if (root == null) return null;
    final data = root['data'] is Map ? root['data'] as Map : root;
    final version = data['version'];
    final build = data['build'];
    final url = data['url'];
    final sha = data['sha256'];
    final size = data['size'];
    final min = data['min_supported_build'];
    final notes = data['notes'];
    final released = data['released_at'];
    if (version is! String || url is! String || sha is! String || notes is! String) return null;
    final buildN = build is int ? build : int.tryParse('$build');
    final sizeN = size is int ? size : int.tryParse('$size');
    final minN = min is int ? min : int.tryParse('$min');
    final when = released is String ? DateTime.tryParse(released) : null;
    if (buildN == null || sizeN == null || minN == null || when == null) return null;
    if (!_sha(sha) || (data['cert_sha256'] is String && !_sha(data['cert_sha256'] as String))) return null;
    return AppReleaseInfo(
      version: version,
      build: buildN,
      url: url,
      sha256: sha.toLowerCase(),
      size: sizeN,
      releasedAt: when.toUtc(),
      minSupportedBuild: minN,
      notes: notes,
      certSha256: data['cert_sha256'] is String ? (data['cert_sha256'] as String).toLowerCase() : null,
    );
  }
}

bool _sha(String value) => RegExp(r'^[0-9a-fA-F]{64}$').hasMatch(value);

/// 只接受 yhvpn.io 和 api.yhvpn.io 的 https 地址，不接受账号、非常用端口或其他域名。
bool releaseUrlAllowed(Uri uri) {
  if (uri.scheme != 'https' || uri.userInfo.isNotEmpty || uri.host.isEmpty) return false;
  final host = uri.host.toLowerCase();
  const allowed = {'yhvpn.io', 'www.yhvpn.io', 'api.yhvpn.io'};
  if (!allowed.contains(host)) return false;
  final port = uri.port;
  return port == 443;
}

UpdateKind decideUpdate({required int localBuild, required AppReleaseInfo latest}) {
  if (localBuild < latest.minSupportedBuild) return UpdateKind.forced;
  if (localBuild < latest.build) return UpdateKind.optional;
  return UpdateKind.current;
}

const windowsInstallerArgs = ['/SILENT', '/CLOSEAPPLICATIONS', '/RESTARTAPPLICATIONS'];

/// 只用 URL 最后一段当文件名，去掉路径和奇怪字符，避免写到更新目录外面。
String releaseFileName(Uri uri) {
  final raw = uri.pathSegments.isEmpty ? '' : uri.pathSegments.last;
  final cleaned = raw.replaceAll(RegExp(r'[^A-Za-z0-9._-]'), '_');
  if (cleaned.isEmpty || cleaned == '.' || cleaned == '..') return 'yuhai-update.bin';
  return cleaned;
}
