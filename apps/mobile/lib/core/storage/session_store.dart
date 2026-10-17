import 'dart:convert';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// 本地会话（方案 3.9）：设备标识、账户号、令牌存安全存储；账户密钥只在备份页展示，不落盘。
class Session {
  const Session(
      {required this.accountNo,
      required this.deviceId,
      required this.accessToken,
      required this.refreshToken,
      required this.inviteCode});
  final String accountNo;
  final String deviceId;
  final String accessToken;
  final String refreshToken;
  final String inviteCode;

  Map<String, dynamic> toJson() => {
        'account_no': accountNo,
        'device_id': deviceId,
        'access_token': accessToken,
        'refresh_token': refreshToken,
        'invite_code': inviteCode
      };
  static Session fromJson(Map<String, dynamic> j) => Session(
      accountNo: j['account_no'],
      deviceId: j['device_id'],
      accessToken: j['access_token'],
      refreshToken: j['refresh_token'],
      inviteCode: j['invite_code'] ?? '');
  Session copyWith({String? accessToken, String? refreshToken}) => Session(
      accountNo: accountNo,
      deviceId: deviceId,
      accessToken: accessToken ?? this.accessToken,
      refreshToken: refreshToken ?? this.refreshToken,
      inviteCode: inviteCode);
}

class SessionStore {
  SessionStore([FlutterSecureStorage? storage])
      : _s = storage ??
            // macOS 未签名/未配置钥匙串共享时，数据保护钥匙串会报错；用传统钥匙串。
            const FlutterSecureStorage(
                mOptions: MacOsOptions(useDataProtectionKeyChain: false));
  final FlutterSecureStorage _s;
  static const _kDeviceUid = 'device_uid';
  static const _kSession = 'session';
  Future<void> _sessionQueue = Future<void>.value();

  // Serialize the read/compare/write operations so an old in-flight request
  // cannot delete or overwrite a session restored while it was in flight.
  Future<T> _sessionOperation<T>(Future<T> Function() action) {
    final result = _sessionQueue.then((_) => action());
    _sessionQueue =
        result.then<void>((_) {}, onError: (Object _, StackTrace __) {});
    return result;
  }

  Future<String?> deviceUid() => _s.read(key: _kDeviceUid);
  Future<void> saveDeviceUid(String uid) =>
      _s.write(key: _kDeviceUid, value: uid);

  Future<Session?> _readSession() async {
    final raw = await _s.read(key: _kSession);
    return raw == null
        ? null
        : Session.fromJson(jsonDecode(raw) as Map<String, dynamic>);
  }

  Future<Session?> session() => _sessionOperation(_readSession);
  Future<void> saveSession(Session s) => _sessionOperation(
      () => _s.write(key: _kSession, value: jsonEncode(s.toJson())));
  Future<void> clearSession() =>
      _sessionOperation(() => _s.delete(key: _kSession));

  bool _sameLogin(Session? current, Session expected) =>
      current?.accountNo == expected.accountNo &&
      current?.deviceId == expected.deviceId;

  Future<bool> clearSessionIfMatches(Session expected) =>
      _sessionOperation(() async {
        if (!_sameLogin(await _readSession(), expected)) return false;
        await _s.delete(key: _kSession);
        return true;
      });

  Future<bool> saveSessionIfMatches(Session expected, Session updated) =>
      _sessionOperation(() async {
        if (!_sameLogin(await _readSession(), expected)) return false;
        await _s.write(key: _kSession, value: jsonEncode(updated.toJson()));
        return true;
      });
}
