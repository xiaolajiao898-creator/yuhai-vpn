import 'package:flutter/services.dart';

/// 设备密钥（方案 4.1）：P-256 密钥对生成在安全硬件（Android StrongBox/TEE Keystore，iOS Secure Enclave），
/// 私钥不可导出；Dart 侧只拿到 SPKI 公钥与「对摘要签名」的能力。
abstract interface class DeviceKey {
  /// 首次调用生成密钥；返回 SPKI DER 的 Base64。
  Future<String> publicKeySpkiBase64();

  /// 对 SHA-256 摘要做 ECDSA 签名，返回 ASN.1 DER。
  Future<Uint8List> signDigest(Uint8List sha256Digest);

  /// 重置账户 / 设备标识冲突（40901）时重新生成。
  Future<void> regenerate();
}

/// MethodChannel 实现：原生代码见 android/.../DeviceKeyChannel.kt 与 ios/Runner/DeviceKeyChannel.swift。
class NativeDeviceKey implements DeviceKey {
  static const _ch = MethodChannel('yuhai/device_key');

  @override
  Future<String> publicKeySpkiBase64() async => (await _ch.invokeMethod<String>('publicKey'))!;

  @override
  Future<Uint8List> signDigest(Uint8List sha256Digest) async =>
      (await _ch.invokeMethod<Uint8List>('sign', {'digest': sha256Digest}))!;

  @override
  Future<void> regenerate() => _ch.invokeMethod<void>('regenerate');
}
