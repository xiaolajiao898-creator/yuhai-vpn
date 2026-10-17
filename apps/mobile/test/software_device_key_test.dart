import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:pointycastle/export.dart';
import 'package:yuhai_vpn/core/security/software_device_key.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  test('电脑版设备密钥：SPKI 公钥可解析，签名可被验证，重置后换钥', () async {
    FlutterSecureStorage.setMockInitialValues({});
    final key = SoftwareDeviceKey();
    final spki = base64Decode(await key.publicKeySpkiBase64());
    expect(spki.length, 91);
    expect(spki[26], 0x04);
    final domain = ECCurve_prime256v1();
    final q = domain.curve.decodePoint(spki.sublist(26))!;
    final digest = SHA256Digest().process(Uint8List.fromList(utf8.encode('hello')));
    final der = await key.signDigest(digest);
    expect(der[0], 0x30);
    // 解 DER，得到 r、s
    BigInt readInt(List<int> b, int at, void Function(int) next) {
      expect(b[at], 0x02);
      final len = b[at + 1];
      final v = b.sublist(at + 2, at + 2 + len).fold<BigInt>(BigInt.zero, (a, x) => (a << 8) | BigInt.from(x));
      next(at + 2 + len);
      return v;
    }
    var pos = 2;
    final r = readInt(der, pos, (p) => pos = p);
    final s = readInt(der, pos, (p) => pos = p);
    final verifier = ECDSASigner()..init(false, PublicKeyParameter<ECPublicKey>(ECPublicKey(q, domain)));
    expect(verifier.verifySignature(digest, ECSignature(r, s)), isTrue);
    // 同一台电脑重复读取得到同一把公钥
    expect(await SoftwareDeviceKey().publicKeySpkiBase64(), base64Encode(spki));
    await key.regenerate();
    expect(await key.publicKeySpkiBase64(), isNot(base64Encode(spki)));
  });
}
