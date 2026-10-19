import 'dart:io';

import 'package:package_info_plus/package_info_plus.dart';
import 'package:uuid/uuid.dart';

import '../../../core/network/api_client.dart';
import '../../../core/platform/platform_name.dart';
import '../../../core/security/device_key.dart';
import '../../../core/storage/session_store.dart';

/// 认证接口封装：注册 / 恢复 / 设备已满替换（方案 11.4）。
class AuthApi {
  AuthApi(this.client, this.deviceKey, this.store);
  final ApiClient client;
  final DeviceKey deviceKey;
  final SessionStore store;
  bool _restoring = false;

  Future<void> _requireSignedOut() async {
    if (await store.session() != null) {
      throw ApiException(40901, '请先退出当前账户，再恢复账户');
    }
  }

  Future<Map<String, dynamic>> _deviceInfo() async {
    var uid = await store.deviceUid();
    if (uid == null) {
      uid = const Uuid().v4();
      await store.saveDeviceUid(uid);
    }
    final info = await PackageInfo.fromPlatform();
    return {
      'device_uid': uid,
      'device_pubkey': await deviceKey.publicKeySpkiBase64(),
      'platform': devicePlatformName(),
      'model': Platform.operatingSystem, // 阶段一先用系统名；接入 device_info_plus 后替换为机型
      'os_version': Platform.operatingSystemVersion.split(' ').first,
      'app_version': '${info.version}+${info.buildNumber}',
    };
  }

  /// 注册。返回账户密钥（仅此一次），调用方展示备份页后丢弃。
  /// 40901（设备标识已存在）时重新生成设备标识与密钥后重试一次。
  Future<RegisterResult> register(
      {InviteHint? invite, bool retried = false}) async {
    final idem = const Uuid().v4();
    final body = await _deviceInfo();
    if (invite != null) {
      body['invite'] = {'code': invite.code, 'method': invite.method};
    }
    try {
      final d = await client.selfSigned('/v1/auth/register', body,
          idempotencyKey: idem);
      await store.saveSession(Session(
        accountNo: d['account_no'],
        deviceId: d['device_id'],
        accessToken: d['tokens']['access_token'],
        refreshToken: d['tokens']['refresh_token'],
        inviteCode: d['invite_code'],
      ));
      return RegisterResult(
          accountNo: d['account_no'],
          accountSecret: d['account_secret'],
          inviteBound: d['invite_bound'] == true);
    } on ApiException catch (e) {
      if (e.code == 40901 && !retried) {
        await store.saveDeviceUid(const Uuid().v4());
        await deviceKey.regenerate();
        return register(invite: invite, retried: true);
      }
      rethrow;
    }
  }

  /// 恢复。沿用本机设备身份；标识已被其他账户占用时才轮换一次。
  /// 设备已满抛出 [DeviceLimitReached]，携带 restore_ticket 与设备列表。
  Future<void> restore(String accountSecret) async {
    if (_restoring) throw ApiException(40901, '正在恢复账户，请稍候');
    _restoring = true;
    try {
      await _restoreOnce(accountSecret, retried: false);
    } finally {
      _restoring = false;
    }
  }

  Future<void> _restoreOnce(String accountSecret,
      {required bool retried}) async {
    await _requireSignedOut();
    final body = (await _deviceInfo())
      ..['account_secret'] = accountSecret.trim();
    try {
      final d = await client.selfSigned('/v1/auth/restore', body);
      await _saveRestored(d);
    } on ApiException catch (e) {
      final limit = deviceLimitFrom(e);
      if (limit != null) throw limit;
      if (e.code == 40901 && !retried) {
        await _rotateDeviceIdentity();
        return _restoreOnce(accountSecret, retried: true);
      }
      rethrow;
    }
  }

  Future<void> replaceDevice(String ticket, String removeDeviceId) async {
    if (_restoring) throw ApiException(40901, '正在恢复账户，请稍候');
    _restoring = true;
    try {
      await _requireSignedOut();
      final body = (await _deviceInfo())
        ..['restore_ticket'] = ticket
        ..['remove_device_id'] = removeDeviceId;
      await _saveRestored(
          await client.selfSigned('/v1/auth/restore/replace-device', body));
    } finally {
      _restoring = false;
    }
  }

  Future<void> _saveRestored(Map<String, dynamic> d) =>
      store.saveSession(Session(
        accountNo: d['account_no'],
        deviceId: d['device_id'],
        accessToken: d['tokens']['access_token'],
        refreshToken: d['tokens']['refresh_token'],
        inviteCode: '',
      ));

  // ---------------- 地址 + 密码 ----------------

  /// 只有设备标识冲突（40901，已被其他账户占用）时才换一把新身份。
  /// 同一台手机反复登录必须沿用原来的 device_uid 和密钥，否则每登一次就占一台设备。
  Future<void> _rotateDeviceIdentity() async {
    await deviceKey.regenerate();
    await store.saveDeviceUid(const Uuid().v4());
  }

  /// 注册新账户时使用新身份，避免把上一账户的设备标识带过去。
  Future<void> _freshDeviceIdentity() => _rotateDeviceIdentity();

  Future<void> _saveLogin(Map<String, dynamic> d, {String inviteCode = ''}) =>
      store.saveSession(Session(
        accountNo: d['account_no'],
        deviceId: d['device_id'],
        accessToken: d['tokens']['access_token'],
        refreshToken: d['tokens']['refresh_token'],
        inviteCode: (d['invite_code'] as String?) ?? inviteCode,
      ));

  /// 注册：账号为自己的 TRON 地址，密码自己设置。成功后当前设备即登录。
  Future<void> registerWithAddress(String address, String password,
      {InviteHint? invite}) async {
    if (_restoring) throw ApiException(40901, '正在处理，请稍候');
    _restoring = true;
    try {
      await _requireSignedOut();
      await _freshDeviceIdentity();
      final body = (await _deviceInfo())
        ..['address'] = address.trim()
        ..['password'] = password;
      if (invite != null) {
        body['invite'] = {'code': invite.code, 'method': invite.method};
      }
      final d = await client.selfSigned('/v1/auth/address/register', body,
          idempotencyKey: const Uuid().v4());
      await _saveLogin(d);
    } finally {
      _restoring = false;
    }
  }

  /// 地址 + 密码登录。沿用本机设备身份。设备已满抛出 [DeviceLimitReached]。
  Future<void> loginWithPassword(String address, String password) async {
    if (_restoring) throw ApiException(40901, '正在登录，请稍候');
    _restoring = true;
    try {
      await _loginOnce(address, password, retried: false);
    } finally {
      _restoring = false;
    }
  }

  Future<void> _loginOnce(String address, String password,
      {required bool retried}) async {
    await _requireSignedOut();
    final body = (await _deviceInfo())
      ..['address'] = address.trim()
      ..['password'] = password;
    try {
      final d = await client.selfSigned('/v1/auth/address/login', body);
      await _saveLogin(d);
    } on ApiException catch (e) {
      final limit = deviceLimitFrom(e);
      if (limit != null) throw limit;
      if (e.code == 40901 && !retried) {
        await _rotateDeviceIdentity();
        return _loginOnce(address, password, retried: true);
      }
      rethrow;
    }
  }

  /// 忘记密码第一步：拿到官方地址与本次要转的精确金额。
  Future<ResetChallenge> startPasswordReset(String address) async {
    final d = await client.postPublic(
        '/v1/auth/password-reset/start', {'address': address.trim()});
    return ResetChallenge(
      id: d['challenge_id'] as String,
      payTo: d['pay_to'] as String,
      amount: d['amount'] as String,
      expiresAt: DateTime.parse(d['expires_at'] as String).toLocal(),
    );
  }

  /// 忘记密码第二步：链上查到转账后设置新密码并登录本设备。
  /// 还没查到时抛出 code 为 40910 的 [ApiException]，调用方隔几秒再试。
  Future<void> completePasswordReset(
      String challengeId, String newPassword) async {
    if (_restoring) throw ApiException(40901, '正在处理，请稍候');
    _restoring = true;
    try {
      await _requireSignedOut();
      // 同一挑战会轮询多次：只在第一次生成新设备身份，之后沿用，避免每次都换钥匙。
      if (_resetIdentityFor != challengeId) {
        await _freshDeviceIdentity();
        _resetIdentityFor = challengeId;
      }
      final body = (await _deviceInfo())
        ..['challenge_id'] = challengeId
        ..['new_password'] = newPassword;
      final d =
          await client.selfSigned('/v1/auth/password-reset/complete', body);
      await _saveLogin(d);
      _resetIdentityFor = null;
    } finally {
      _restoring = false;
    }
  }

  String? _resetIdentityFor;

  Future<void> changePassword(String oldPassword, String newPassword) =>
      client.call('POST', '/v1/account/password',
          json: {'old_password': oldPassword, 'new_password': newPassword},
          sign: true,
          idempotencyKey: const Uuid().v4());

  Future<void> ackBackup() => client.call('POST', '/v1/account/backup-ack',
      sign: true, idempotencyKey: const Uuid().v4());
}

class InviteHint {
  const InviteHint(this.code, this.method);
  final String code;
  final String method; // APK_CHANNEL / CLIPBOARD / MANUAL
}

class RegisterResult {
  const RegisterResult(
      {required this.accountNo,
      required this.accountSecret,
      required this.inviteBound});
  final String accountNo;
  final String accountSecret;
  final bool inviteBound;
}

class ResetChallenge {
  const ResetChallenge(
      {required this.id,
      required this.payTo,
      required this.amount,
      required this.expiresAt});
  final String id;
  final String payTo;
  final String amount; // TRX，6 位小数，须精确转账
  final DateTime expiresAt;
}

class DeviceLimitReached implements Exception {
  DeviceLimitReached(
      {required this.ticket, required this.devices, this.limit = 0});
  final String ticket;
  final List<Map<String, dynamic>> devices;
  final int limit;

  String get message => limit > 0
      ? '设备数量已达上限（最多 $limit 台）。请选择一台旧设备移除后再登录。'
      : '设备数量已达上限。请选择一台旧设备移除后再登录。';
}

/// 40902 的设备列表解析失败时仍给出明确提示，而不是掉进「网络不可用」。
DeviceLimitReached? deviceLimitFrom(ApiException error) {
  if (error.code != 40902) return null;
  final details = error.details ?? const <String, dynamic>{};
  final raw = details['devices'];
  final devices = <Map<String, dynamic>>[];
  if (raw is List) {
    for (final item in raw) {
      if (item is Map) devices.add(Map<String, dynamic>.from(item));
    }
  }
  final ticket = details['restore_ticket'];
  final limit = details['device_limit'];
  return DeviceLimitReached(
    ticket: ticket is String ? ticket : '',
    devices: devices,
    limit: limit is int ? limit : 0,
  );
}
