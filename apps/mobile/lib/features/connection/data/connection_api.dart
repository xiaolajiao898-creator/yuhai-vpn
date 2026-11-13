import 'package:dio/dio.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../../../app/providers.dart';
import '../../../core/network/api_client.dart';
import '../../../core/storage/session_store.dart';
import '../../../platform/vpn/egress_profile.dart';
import '../../../platform/vpn/wireguard_profile.dart';

String vpnIdentity(Session session) =>
    '${session.accountNo}:${session.deviceId}';

class ConnectionApi {
  ConnectionApi(this.client);
  final ApiClient client;

  Future<void> uploadPublicKey(Session session, String publicKey) async {
    final identity = vpnIdentity(session);
    if (!validVpnIdentity(identity) || !validWireGuardPublicKey(publicKey)) {
      throw const FormatException('VPN 设备公钥无效');
    }
    await client.call('PUT', '/v1/devices/current/wg-key',
        expectedSession: session,
        json: {'wg_pubkey': publicKey},
        sign: true,
        idempotencyKey: const Uuid().v5(
            Namespace.url.value, 'yuhai:wireguard-key:$identity:$publicKey'));
  }

  Future<WireGuardProfile> profile(
      Session session, String nodeId, String publicKey) async {
    final response = await client.call('POST', '/v1/connect/profiles',
        expectedSession: session,
        json: {
          'node_ids': [nodeId],
          'protocols': ['WIREGUARD']
        },
        sign: true);
    return WireGuardProfile.parseResponse(response,
        nodeId: nodeId,
        identity: vpnIdentity(session),
        clientPublicKey: publicKey,
        now: DateTime.now().toUtc(),
        clockOffsetSeconds: client.clockOffsetSeconds);
  }
}

extension EgressConnectionApi on ConnectionApi {
  /// 出口连接材料（含凭据）：每次连接现取，不缓存。
  Future<EgressProfile> egressProfile(Session session, String egressId) async {
    if (!validEgressId(egressId)) throw const FormatException('出口编号无效');
    final response = await client.call('POST', '/v1/connect/egress',
        expectedSession: session, json: {'egress_id': egressId}, sign: true);
    return EgressProfile.parseResponse(response,
        egressId: egressId,
        identity: vpnIdentity(session),
        now: DateTime.now().toUtc(),
        clockOffsetSeconds: client.clockOffsetSeconds);
  }
}

extension WorldVPNConnectionApi on ConnectionApi {
  /// 批发线路的 WireGuard 配置。私钥只在这一次响应里，不缓存。
  Future<WireGuardProfile> worldvpnProfile(
      Session session, String lineId) async {
    if (!RegExp(r'^w[1-9][0-9]*$').hasMatch(lineId)) {
      throw const FormatException('线路编号无效');
    }
    // 批发开通和换线经常要 15 秒以上。只放宽这一次请求，其它接口仍是 15 秒。
    // 超时再试一次：服务端会清掉没建完的 peer，重复提交不会多占一台设备。
    const timeout = Duration(seconds: 45);
    Future<Map<String, dynamic>> once() => client.call(
        'POST', '/v1/connect/worldvpn',
        expectedSession: session,
        json: {'line_id': lineId},
        sign: true,
        timeout: timeout);
    Map<String, dynamic> response;
    try {
      response = await once();
    } on DioException catch (error) {
      if (!isConnectTimeout(error)) rethrow;
      response = await once();
    }
    return WireGuardProfile.parseIssued(response,
        lineId: lineId,
        identity: vpnIdentity(session),
        now: DateTime.now().toUtc(),
        clockOffsetSeconds: client.clockOffsetSeconds);
  }

  Future<void> releaseWorldVPN(Session session) async {
    await client.call('POST', '/v1/connect/worldvpn/release',
        expectedSession: session, json: const {}, sign: true);
  }

  /// 连接失败时把阶段、错误原文和服务日志摘要交给服务端。调用方要先去掉私钥。
  Future<void> reportTunnelDiagnostic(
    Session session, {
    required String stage,
    required String errorCode,
    required String errorText,
    required String serviceState,
    required String logSummary,
  }) async {
    await client.call('POST', '/v1/connect/diagnostics',
        expectedSession: session,
        json: {
          'stage': stage,
          'error_code': errorCode,
          'error_text': errorText,
          'service_state': serviceState,
          'log_summary': logSummary,
        },
        sign: true,
        timeout: const Duration(seconds: 8));
  }
}

bool isConnectTimeout(DioException error) =>
    error.type == DioExceptionType.receiveTimeout ||
    error.type == DioExceptionType.sendTimeout ||
    error.type == DioExceptionType.connectionTimeout;

final connectionApiProvider = Provider<ConnectionApi>(
    (ref) => ConnectionApi(ref.watch(apiClientProvider)));
