import 'dart:io';

import 'package:flutter/foundation.dart';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../core/network/api_client.dart';
import 'api_fallback_debug.dart'
    if (dart.vm.product) 'api_fallback_release.dart';
import '../core/platform/platform_name.dart';
import '../core/security/device_key.dart';
import '../core/security/software_device_key.dart';
import '../core/storage/app_secure_storage.dart';
import '../core/storage/durable_id_store.dart';
import '../core/storage/session_store.dart';

/// 全局依赖注入。测试时用 ProviderScope(overrides: ...) 替换。
/// 发行包必须传入 YH_API_BASE。默认值留空，避免把开发环境地址编进真机安装包。
const apiBaseUrl = String.fromEnvironment('YH_API_BASE');
const appVersion = '1.0.0+100';

bool isSecureApiBaseUrl(String value, {bool debug = kDebugMode}) {
  final uri = Uri.tryParse(value);
  if (uri == null ||
      value != value.trim() ||
      uri.host.isEmpty ||
      uri.userInfo.isNotEmpty ||
      uri.hasQuery ||
      uri.hasFragment ||
      (uri.path.isNotEmpty && uri.path != '/') ||
      uri.port < 1 ||
      uri.port > 65535) {
    return false;
  }
  final host = uri.host.toLowerCase();
  const developmentHosts = {'10.0.2.2', '127.0.0.1', 'localhost'};
  if (debug && developmentHosts.contains(host)) {
    return uri.scheme == 'http' || uri.scheme == 'https';
  }
  if (uri.scheme != 'https' ||
      developmentHosts.contains(host) ||
      InternetAddress.tryParse(host) != null ||
      !host.contains('.') ||
      host.endsWith('.localhost') ||
      host.endsWith('.local') ||
      host.endsWith('.test') ||
      host.endsWith('.invalid')) {
    return false;
  }
  return host.split('.').every((label) =>
      RegExp(r'^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$').hasMatch(label));
}

String checkedApiBaseUrl() {
  final value = apiBaseUrl.isNotEmpty ? apiBaseUrl : debugApiFallback;
  if (!isSecureApiBaseUrl(value, debug: kDebugMode)) {
    throw StateError('正式构建必须使用有效的 HTTPS API 域名');
  }
  return value;
}

final sessionStoreProvider = Provider<SessionStore>(
    (ref) => SessionStore(appSecureStorage, ref.watch(durableIdStoreProvider)));
final deviceKeyProvider = Provider<DeviceKey>((_) => !kIsWeb &&
        (defaultTargetPlatform == TargetPlatform.windows ||
            defaultTargetPlatform == TargetPlatform.macOS)
    ? SoftwareDeviceKey()
    : NativeDeviceKey());
final apiClientProvider = Provider<ApiClient>((ref) => ApiClient(
      baseUrl: checkedApiBaseUrl(),
      deviceKey: ref.watch(deviceKeyProvider),
      store: ref.watch(sessionStoreProvider),
      appVersion: appVersion,
      platform: devicePlatformName(),
    ));

/// 资金系统收款地址签名公钥（Ed25519, Base64），内置两把以支持轮换（方案 6.8）。
/// 公钥与密钥编号都通过 --dart-define 注入；编号必须与服务端 FUND_ADDRESS_KEY_ID
/// 以及签名时的 -key-id 一致（生产为 fund-prod-1，开发可传 fund-dev-1）。
const fundAddressKeyId1 =
    String.fromEnvironment('YH_FUND_KEY_ID_1', defaultValue: 'fund-prod-1');
const fundAddressKeyId2 =
    String.fromEnvironment('YH_FUND_KEY_ID_2', defaultValue: 'fund-prod-2');
const fundAddressPubKeys = <String, String>{
  fundAddressKeyId1: String.fromEnvironment('YH_FUND_PUBKEY_1'),
  fundAddressKeyId2: String.fromEnvironment('YH_FUND_PUBKEY_2'),
};
