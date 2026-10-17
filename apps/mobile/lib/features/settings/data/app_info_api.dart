import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../../app/providers.dart';
import '../../../core/network/api_client.dart';

class AppRelease {
  AppRelease.fromJson(Map<String, dynamic> json)
      : name = json['latest']['version_name'] as String,
        code = json['latest']['version_code'] as int,
        notes = json['latest']['notes'] as String? ?? '',
        forceUpdate = json['force_update'] == true,
        downloadUrl = safeReleaseLink(json['latest']['download_url']);
  final String name, notes;
  final int code;
  final bool forceUpdate;
  final String? downloadUrl;
}

String? safeReleaseLink(Object? value) {
  if (value is! String) return null;
  try {
    final uri = Uri.tryParse(value);
    return uri != null &&
            uri.scheme == 'https' &&
            uri.host.isNotEmpty &&
            uri.userInfo.isEmpty && uri.port > 0 && uri.port <= 65535
        ? uri.toString()
        : null;
  } on FormatException {
    return null;
  }
}

class AppInfoApi {
  AppInfoApi(this.client);
  final ApiClient client;
  Future<AppRelease> latest() async =>
      AppRelease.fromJson(await client.getPublic('/app/version', query: {
        'platform': [
          defaultTargetPlatform == TargetPlatform.iOS ? 'IOS' : 'ANDROID'
        ],
        'current_code': [appVersion.split('+').last],
      }));
}

final appInfoApiProvider =
    Provider<AppInfoApi>((ref) => AppInfoApi(ref.watch(apiClientProvider)));
