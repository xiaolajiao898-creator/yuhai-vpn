import 'dart:convert';

import 'package:cryptography/cryptography.dart';

/// 收款地址防篡改校验（方案 6.8）：签名原文 `yuhai-addr-v1|{network}|{address}|{valid_until}`，
/// 用 App 内置公钥验证。失败时页面不展示地址。
class AddressProof {
  const AddressProof({required this.keyId, required this.validUntil, required this.signature});
  final String keyId;
  final int validUntil;
  final String signature; // Base64url（无填充）

  static AddressProof? fromJson(Map<String, dynamic>? j) =>
      j == null ? null : AddressProof(keyId: j['key_id'], validUntil: j['valid_until'], signature: j['signature']);

  static String message(String network, String address, int validUntil) => 'yuhai-addr-v1|$network|$address|$validUntil';

  /// [pubKeys] key_id → Base64 公钥。
  Future<bool> verify({required String network, required String address, required Map<String, String> pubKeys, DateTime? now}) async {
    final pk = pubKeys[keyId];
    if (pk == null || pk.isEmpty) return false;
    if ((now ?? DateTime.now().toUtc()).millisecondsSinceEpoch ~/ 1000 > validUntil) return false;
    try {
      final key = SimplePublicKey(base64Decode(pk), type: KeyPairType.ed25519);
      final sigBytes = base64Url.decode(base64Url.normalize(signature));
      return await Ed25519().verify(utf8.encode(message(network, address, validUntil)), signature: Signature(sigBytes, publicKey: key));
    } catch (_) {
      return false;
    }
  }
}
