import 'dart:convert';
import 'dart:typed_data';

import 'package:crypto/crypto.dart';

/// 请求签名规范化（方案 12.2），必须与 server/internal/auth/signature.go 字节级一致。
/// 查询参数编码采用 application/x-www-form-urlencoded 规则（空格 → +），与 Go 的 url.QueryEscape 相同。
abstract final class RequestSigner {
  static const algorithm = 'YH1-ECDSA-P256-SHA256';

  static String canonical({
    required String method,
    required String path,
    required Map<String, List<String>> query,
    required String timestamp,
    required String nonce,
    required String deviceId,
    required Uint8List body,
  }) {
    return [
      algorithm,
      method.toUpperCase(),
      path.isEmpty ? '/' : path,
      _canonicalQuery(query),
      timestamp,
      nonce,
      deviceId,
      sha256.convert(body).toString(),
    ].join('\n');
  }

  static String _canonicalQuery(Map<String, List<String>> q) {
    if (q.isEmpty) return '';
    final keys = q.keys.toList()..sort();
    final parts = <String>[];
    for (final k in keys) {
      final vals = [...q[k]!]..sort();
      for (final v in vals) {
        parts.add('${Uri.encodeQueryComponent(k)}=${Uri.encodeQueryComponent(v)}');
      }
    }
    return parts.join('&');
  }

  /// 签名摘要 = SHA-256(canonical)；由安全硬件中的 P-256 私钥做 ECDSA（DER）签名，Base64 放入 X-Signature。
  static Uint8List digest(String canonical) => Uint8List.fromList(sha256.convert(utf8.encode(canonical)).bytes);
}
