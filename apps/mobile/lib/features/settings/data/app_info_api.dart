import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../app/providers.dart';
import '../../../core/network/api_client.dart';

class AppRelease {
  AppRelease({
    required this.name,
    required this.code,
    required this.notes,
    required this.forceUpdate,
    required this.downloadUrl,
  });

  /// `GET /v1/app/latest` 的 data：version、build、url、notes、min_supported_build。
  factory AppRelease.fromLatest(Map<String, dynamic> json,
      {required int currentBuild}) {
    final name = json['version'];
    final notes = json['notes'];
    final build = _jsonInt(json['build']);
    final minBuild = _jsonInt(json['min_supported_build']);
    if (name is! String || name.isEmpty || notes is! String || build == null) {
      throw const FormatException('更新信息无效');
    }
    return AppRelease(
      name: name,
      code: build,
      notes: notes,
      forceUpdate: minBuild != null && currentBuild < minBuild,
      downloadUrl: safeReleaseLink(json['url']),
    );
  }

  final String name, notes;
  final int code;
  final bool forceUpdate;
  final String? downloadUrl;
}

int? _jsonInt(Object? value) {
  if (value is int) return value;
  if (value is num && value == value.roundToDouble()) return value.toInt();
  return null;
}

String? safeReleaseLink(Object? value) {
  if (value is! String) return null;
  try {
    final uri = Uri.tryParse(value);
    return uri != null &&
            uri.scheme == 'https' &&
            uri.host.isNotEmpty &&
            uri.userInfo.isEmpty &&
            uri.port > 0 &&
            uri.port <= 65535
        ? uri.toString()
        : null;
  } on FormatException {
    return null;
  }
}

/// 检查更新只认当前安装包接口。windows 与 android 用小写平台名。
String? latestAppPlatform() {
  switch (defaultTargetPlatform) {
    case TargetPlatform.windows:
      return 'windows';
    case TargetPlatform.android:
      return 'android';
    default:
      return null;
  }
}

class AppInfoApi {
  AppInfoApi(this.client);
  final ApiClient client;
  Future<AppRelease> latest() async {
    final platform = latestAppPlatform();
    if (platform == null) {
      throw const FormatException('这个系统还不能在应用里检查更新');
    }
    final current = int.tryParse(appVersion.split('+').last) ?? 0;
    return AppRelease.fromLatest(
        await client.getPublic('/v1/app/latest', query: {
          'platform': [platform],
        }),
        currentBuild: current);
  }
}

final appInfoApiProvider =
    Provider<AppInfoApi>((ref) => AppInfoApi(ref.watch(apiClientProvider)));
