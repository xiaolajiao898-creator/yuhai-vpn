import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';

import 'package:flutter_secure_storage/flutter_secure_storage.dart';
import 'package:pointycastle/export.dart';

import 'device_key.dart';

/// 电脑版的设备密钥：P-256 私钥由系统凭据保护保存（Windows 凭据保护 / macOS 钥匙串），
/// 签名在 Dart 里完成（确定性 ECDSA，RFC 6979）。手机上仍使用安全硬件里的 [NativeDeviceKey]。
class SoftwareDeviceKey implements DeviceKey {
  SoftwareDeviceKey([FlutterSecureStorage? storage])
      : _s = storage ??
            const FlutterSecureStorage(
                mOptions: MacOsOptions(useDataProtectionKeyChain: false));

  static const _kKey = 'device_key_p256_v1';
  static final _domain = ECCurve_prime256v1();
  // SubjectPublicKeyInfo 前缀：id-ecPublicKey + prime256v1，后接 65 字节未压缩公钥。
  static final _spkiPrefix = Uint8List.fromList([
    0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, //
    0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
  ]);

  final FlutterSecureStorage _s;
  BigInt? _d;

  Future<BigInt> _privateKey() async {
    if (_d != null) return _d!;
    final stored = await _s.read(key: _kKey);
    final parsed = stored == null ? null : BigInt.tryParse(stored, radix: 16);
    if (parsed != null && parsed > BigInt.zero && parsed < _domain.n) {
      return _d = parsed;
    }
    return _generate();
  }

  Future<BigInt> _generate() async {
    final rng = Random.secure();
    BigInt d;
    do {
      final bytes = List<int>.generate(32, (_) => rng.nextInt(256));
      d = bytes.fold<BigInt>(BigInt.zero, (a, b) => (a << 8) | BigInt.from(b));
    } while (d <= BigInt.zero || d >= _domain.n);
    await _s.write(key: _kKey, value: d.toRadixString(16));
    return _d = d;
  }

  static Uint8List _uint(BigInt v, int len) {
    final out = Uint8List(len);
    var x = v;
    for (var i = len - 1; i >= 0; i--) {
      out[i] = (x & BigInt.from(0xff)).toInt();
      x = x >> 8;
    }
    return out;
  }

  @override
  Future<String> publicKeySpkiBase64() async {
    final d = await _privateKey();
    final q = (_domain.G * d)!;
    final encoded = q.getEncoded(false);
    return base64Encode([..._spkiPrefix, ...encoded]);
  }

  @override
  Future<Uint8List> signDigest(Uint8List sha256Digest) async {
    final d = await _privateKey();
    final signer = ECDSASigner(null, HMac(SHA256Digest(), 64))
      ..init(true, PrivateKeyParameter<ECPrivateKey>(ECPrivateKey(d, _domain)));
    final sig = signer.generateSignature(sha256Digest) as ECSignature;
    return _der(sig.r, sig.s);
  }

  @override
  Future<void> regenerate() async {
    _d = null;
    await _generate();
  }

  static Uint8List _derInt(BigInt v) {
    var bytes = _uint(v, 32).toList();
    while (bytes.length > 1 && bytes[0] == 0 && bytes[1] < 0x80) {
      bytes = bytes.sublist(1);
    }
    if (bytes[0] >= 0x80) bytes = [0, ...bytes];
    return Uint8List.fromList([0x02, bytes.length, ...bytes]);
  }

  static Uint8List _der(BigInt r, BigInt s) {
    final body = [..._derInt(r), ..._derInt(s)];
    return Uint8List.fromList([0x30, body.length, ...body]);
  }

}
