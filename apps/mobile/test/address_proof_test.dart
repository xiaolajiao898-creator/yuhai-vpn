import 'dart:convert';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/security/address_proof.dart';

void main() {
  test('verify signed address; reject tampered / expired / unknown key', () async {
    final algo = Ed25519();
    final kp = await algo.newKeyPair();
    final pub = base64Encode((await kp.extractPublicKey()).bytes);
    final until = DateTime.utc(2030).millisecondsSinceEpoch ~/ 1000;
    final msg = AddressProof.message('MOCK', 'MOCKADDR1', until);
    final sig = await algo.sign(utf8.encode(msg), keyPair: kp);
    final proof = AddressProof(keyId: 'k1', validUntil: until, signature: base64Url.encode(sig.bytes).replaceAll('=', ''));
    final keys = {'k1': pub};
    expect(await proof.verify(network: 'MOCK', address: 'MOCKADDR1', pubKeys: keys), isTrue);
    expect(await proof.verify(network: 'MOCK', address: 'MOCKADDR2', pubKeys: keys), isFalse);
    expect(await proof.verify(network: 'MOCK', address: 'MOCKADDR1', pubKeys: {'k2': pub}), isFalse);
    expect(await proof.verify(network: 'MOCK', address: 'MOCKADDR1', pubKeys: keys, now: DateTime.utc(2031)), isFalse);
  });
}
