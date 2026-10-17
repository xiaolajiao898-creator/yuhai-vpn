import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:dio/dio.dart';

import '../security/device_key.dart';
import '../storage/session_store.dart';
import 'request_signer.dart';

/// 统一 API 客户端（方案 11.1–11.2）：
/// - 响应 { data, request_id } / { error, request_id }
/// - S2 请求自动附加 X-Device-Id / X-Timestamp / X-Nonce / X-Signature；S2-self 用于注册/恢复
/// - 40101 时静默刷新一次；40105 用 server_time 校准时钟；40103/40107/40108 清会话
/// - 证书固定与备用域名切换在第 3–4 周补入拦截器
class ApiClient {
  ApiClient(
      {required String baseUrl,
      required this.deviceKey,
      required this.store,
      required this.appVersion,
      required this.platform})
      : dio = Dio(BaseOptions(
          baseUrl: baseUrl,
          followRedirects: false,
          sendTimeout: const Duration(seconds: 15),
          connectTimeout: const Duration(seconds: 10),
          receiveTimeout: const Duration(seconds: 15),
          headers: {
            'X-App-Version': appVersion,
            'X-Platform': platform,
            'Accept-Language': 'zh-CN'
          },
          validateStatus: (_) => true,
        ));

  final Dio dio;
  final DeviceKey deviceKey;
  final SessionStore store;
  final String appVersion;
  final String platform;
  int clockOffsetSeconds = 0;
  final _rng = Random.secure();
  Future<void>? _refreshInFlight;
  Session? _refreshSession;

  /// 会话失效回调（40103 / 40107 / 40108）：由 App 层跳转到恢复账户页
  void Function(int code)? onSessionLost;

  /// Public metadata requests never carry account tokens or device signatures.
  Future<Map<String, dynamic>> getPublic(String path,
      {Map<String, List<String>> query = const {}}) async {
    final response = await dio.get<dynamic>(path,
        queryParameters:
            query.map((k, v) => MapEntry(k, v.length == 1 ? v.first : v)),
        options: Options(responseType: ResponseType.json));
    return _unwrap(response);
  }

  /// 无需登录、也不需要设备签名的公开写接口（例如发起找回密码）。不携带任何令牌。
  Future<Map<String, dynamic>> postPublic(
      String path, Map<String, dynamic> json) async {
    final response = await dio.post<dynamic>(path,
        data: utf8.encode(jsonEncode(json)),
        options: Options(
            headers: {'Content-Type': 'application/json'},
            responseType: ResponseType.json));
    return _unwrap(response);
  }

  Future<void> _sessionLost(Session expected, int code) async {
    if (await store.clearSessionIfMatches(expected)) {
      onSessionLost?.call(code);
    }
  }

  bool _sameLogin(Session? actual, Session expected) =>
      actual?.accountNo == expected.accountNo &&
      actual?.deviceId == expected.deviceId;

  Future<Session> _sessionFor(Session expected) async {
    final current = await store.session();
    if (!_sameLogin(current, expected)) {
      throw ApiException(40101, '登录状态已变化，请重新操作');
    }
    return current!;
  }

  String _nonce() =>
      base64UrlEncode(List<int>.generate(16, (_) => _rng.nextInt(256)))
          .replaceAll('=', '');

  Future<Map<String, String>> _sigHeaders(String method, String path,
      Map<String, List<String>> query, Uint8List body, String deviceUid) async {
    final ts = (DateTime.now().toUtc().millisecondsSinceEpoch ~/ 1000 +
            clockOffsetSeconds)
        .toString();
    final nonce = _nonce();
    final canonical = RequestSigner.canonical(
        method: method,
        path: path,
        query: query,
        timestamp: ts,
        nonce: nonce,
        deviceId: deviceUid,
        body: body);
    final sig = await deviceKey.signDigest(RequestSigner.digest(canonical));
    return {
      'X-Device-Id': deviceUid,
      'X-Timestamp': ts,
      'X-Nonce': nonce,
      'X-Signature': base64Encode(sig)
    };
  }

  /// S2-self：注册 / 恢复。请求体自带公钥，签名证明持有私钥。
  Future<Map<String, dynamic>> selfSigned(
      String path, Map<String, dynamic> json,
      {String? idempotencyKey}) async {
    final body = Uint8List.fromList(utf8.encode(jsonEncode(json)));
    final headers = await _sigHeaders(
        'POST', path, const {}, body, json['device_uid'] as String);
    if (idempotencyKey != null) headers['Idempotency-Key'] = idempotencyKey;
    headers['Content-Type'] = 'application/json';
    final res = await dio.post<dynamic>(path,
        data: body,
        options: Options(headers: headers, responseType: ResponseType.json));
    return _unwrap(res);
  }

  /// S1 / S2：已登录请求。sign=true 附加设备签名（写操作与连接配置）。
  Future<Map<String, dynamic>> call(String method, String path,
      {Map<String, dynamic>? json,
      Map<String, List<String>> query = const {},
      bool sign = false,
      String? idempotencyKey,
      bool retrying = false,
      Session? expectedSession}) async {
    final session = expectedSession ?? await store.session();
    if (session == null) {
      throw ApiException(40101, '未登录');
    }
    return _callForSession(session, method, path,
        json: json,
        query: query,
        sign: sign,
        idempotencyKey: idempotencyKey,
        retrying: retrying);
  }

  Future<Map<String, dynamic>> _callForSession(
      Session expected, String method, String path,
      {Map<String, dynamic>? json,
      Map<String, List<String>> query = const {},
      bool sign = false,
      String? idempotencyKey,
      required bool retrying}) async {
    final session = await _sessionFor(expected);
    final deviceUid = await store.deviceUid();
    final body = json == null
        ? Uint8List(0)
        : Uint8List.fromList(utf8.encode(jsonEncode(json)));
    final headers = <String, String>{
      'Authorization': 'Bearer ${session.accessToken}'
    };
    if (sign) {
      headers.addAll(await _sigHeaders(method, path, query, body, deviceUid!));
    }
    if (idempotencyKey != null) headers['Idempotency-Key'] = idempotencyKey;
    if (json != null) headers['Content-Type'] = 'application/json';
    await _sessionFor(expected);
    final res = await dio.request<dynamic>(path,
        data: json == null ? null : body,
        queryParameters:
            query.map((k, v) => MapEntry(k, v.length == 1 ? v.first : v)),
        options: Options(
            method: method, headers: headers, responseType: ResponseType.json));
    await _sessionFor(expected);
    try {
      return _unwrap(res);
    } on ApiException catch (e) {
      if (e.code == 40101 && !retrying) {
        await _sessionFor(expected);
        await _refreshForSession(expected);
        await _sessionFor(expected);
        return _callForSession(expected, method, path,
            json: json,
            query: query,
            sign: sign,
            idempotencyKey: idempotencyKey,
            retrying: true);
      }
      if (e.code == 40105 && !retrying) {
        await _sessionFor(expected);
        final st = DateTime.tryParse(e.details?['server_time'] ?? '');
        if (st != null) {
          clockOffsetSeconds = st.difference(DateTime.now().toUtc()).inSeconds;
        }
        return _callForSession(expected, method, path,
            json: json,
            query: query,
            sign: sign,
            idempotencyKey: idempotencyKey,
            retrying: true);
      }
      if (e.code == 40103 || e.code == 40107 || e.code == 40108) {
        await _sessionLost(session, e.code);
      }
      rethrow;
    }
  }

  /// 条件 GET：携带 If-None-Match，未变化返回 null（304）。用于支付页 3 秒轮询。
  Future<(Map<String, dynamic>?, String?)> getIfChanged(String path,
      {String? etag}) async {
    final session = await store.session();
    if (session == null) {
      throw ApiException(40101, '未登录');
    }
    return _getIfChangedForSession(session, path, etag: etag, retrying: false);
  }

  Future<(Map<String, dynamic>?, String?)> _getIfChangedForSession(
      Session expected, String path,
      {String? etag, required bool retrying}) async {
    final session = await _sessionFor(expected);
    final headers = <String, String>{
      'Authorization': 'Bearer ${session.accessToken}',
      if (etag != null) 'If-None-Match': etag
    };
    final res = await dio.get<dynamic>(path,
        options: Options(headers: headers, responseType: ResponseType.json));
    await _sessionFor(expected);
    if (res.statusCode == 304) return (null, etag);
    try {
      return (_unwrap(res), res.headers.value('etag'));
    } on ApiException catch (e) {
      if (e.code == 40101 && !retrying) {
        await _sessionFor(expected);
        await _refreshForSession(expected);
        await _sessionFor(expected);
        return _getIfChangedForSession(expected, path,
            etag: etag, retrying: true);
      }
      if (e.code == 40103 || e.code == 40107 || e.code == 40108) {
        await _sessionLost(session, e.code);
      }
      rethrow;
    }
  }

  /// 刷新令牌：S2（访问令牌可过期）。旧令牌重放会导致 40103。
  Future<void> refresh() async {
    final pending = _refreshInFlight;
    final pendingSession = _refreshSession;
    final session = await store.session();
    if (session == null) {
      throw ApiException(40101, '未登录');
    }
    if (pending != null && _sameLogin(pendingSession, session)) {
      return pending;
    }
    return _refreshForSession(session);
  }

  Future<void> _refreshForSession(Session expected) async {
    final current = await _sessionFor(expected);
    // Another request may have finished refreshing while this caller was
    // checking its login. Its new tokens are sufficient for the one retry.
    if (current.refreshToken != expected.refreshToken) {
      return;
    }
    final pending = _refreshInFlight;
    if (pending != null && _sameLogin(_refreshSession, expected)) {
      return pending;
    }
    final future = _refreshOnce(expected);
    _refreshInFlight = future;
    _refreshSession = expected;
    return future.whenComplete(() {
      if (identical(_refreshInFlight, future)) {
        _refreshInFlight = null;
        _refreshSession = null;
      }
    });
  }

  Future<void> _refreshOnce(Session expected) async {
    final session = await _sessionFor(expected);
    final deviceUid = (await store.deviceUid())!;
    const path = '/v1/auth/refresh';
    final body = Uint8List.fromList(
        utf8.encode(jsonEncode({'refresh_token': session.refreshToken})));
    final headers = await _sigHeaders('POST', path, const {}, body, deviceUid);
    headers['Authorization'] = 'Bearer ${session.accessToken}';
    headers['Content-Type'] = 'application/json';
    await _sessionFor(expected);
    final res = await dio.post<dynamic>(path,
        data: body,
        options: Options(headers: headers, responseType: ResponseType.json));
    try {
      final data = _unwrap(res);
      await store.saveSessionIfMatches(
          session,
          session.copyWith(
              accessToken: data['access_token'],
              refreshToken: data['refresh_token']));
    } on ApiException catch (e) {
      if (e.code == 40103 || e.code == 40107 || e.code == 40108) {
        await _sessionLost(session, e.code);
      }
      rethrow;
    }
  }

  Map<String, dynamic> _unwrap(Response<dynamic> res) {
    final status = res.statusCode ?? 0;
    if (status == 204) return const {};
    final json = res.data is Map
        ? res.data as Map<String, dynamic>
        : <String, dynamic>{};
    if (status >= 200 && status < 300) {
      _syncClock(json['data']);
      return (json['data'] as Map<String, dynamic>?) ?? const {};
    }
    final err = json['error'] as Map<String, dynamic>?;
    throw ApiException(
        err?['code'] ?? status * 100 + 1, err?['message'] ?? '请求失败',
        details: (err?['details'] as Map?)?.cast<String, dynamic>(),
        requestId: json['request_id'] as String?);
  }

  void _syncClock(dynamic data) {
    if (data is Map && data['server_time'] is String) {
      final st = DateTime.tryParse(data['server_time'] as String);
      if (st != null) {
        clockOffsetSeconds = st.difference(DateTime.now().toUtc()).inSeconds;
      }
    }
  }
}

/// 业务错误：code 为 5 位错误码（方案 11.7）。
class ApiException implements Exception {
  ApiException(this.code, this.message, {this.details, this.requestId});
  final int code;
  final String message;
  final Map<String, dynamic>? details;
  final String? requestId;

  @override
  String toString() => 'ApiException($code, $message, request_id=$requestId)';
}
