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

final connectionApiProvider = Provider<ConnectionApi>(
    (ref) => ConnectionApi(ref.watch(apiClientProvider)));
