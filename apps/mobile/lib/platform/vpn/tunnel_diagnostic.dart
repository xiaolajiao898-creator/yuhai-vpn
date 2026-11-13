/// 连接失败时交给服务端的一份诊断。私钥和预共享密钥必须先去掉。
class TunnelDiagnostic {
  const TunnelDiagnostic({
    required this.stage,
    required this.errorCode,
    required this.errorText,
    required this.serviceState,
    required this.logSummary,
  });

  /// prepare / install / service / handshake / connect
  final String stage;
  final String errorCode;
  final String errorText;
  final String serviceState;
  final String logSummary;
}

/// 能在配置交给系统之后报告握手结果的引擎。Windows 隧道服务走这条路径。
abstract interface class HandshakeReporter {
  bool get reportsHandshake;
  TunnelDiagnostic? get tunnelDiagnostic;
}

final _wireGuardKey = RegExp(r'[A-Za-z0-9+/]{43}=');
final _privateKeyLine =
    RegExp(r'(private\s*key\s*[=:]\s*)\S+', caseSensitive: false);

/// 去掉 WireGuard 私钥（44 位标准编码）和 PrivateKey 行，并只保留日志尾部。
String redactTunnelText(String raw) {
  var text = raw.replaceAll(_wireGuardKey, '[redacted]');
  text = text.replaceAllMapped(_privateKeyLine, (match) => '${match[1]}[redacted]');
  final lines = text.split(RegExp(r'\r?\n'));
  if (lines.length > 40) {
    text = lines.sublist(lines.length - 40).join('\n');
  }
  if (text.length > 3500) {
    text = text.substring(text.length - 3500);
  }
  return text;
}
