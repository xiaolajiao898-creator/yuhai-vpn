import 'dart:io';

/// 本机诊断日志。注册和登录的异常写在这里，不进界面，也不带密码。
class ClientLog {
  const ClientLog._();

  static Future<File> file() async {
    final override = Platform.environment['YH_CLIENT_LOG'];
    if (override != null && override.trim().isNotEmpty) {
      final named = File(override.trim());
      await named.parent.create(recursive: true);
      return named;
    }
    final Directory dir;
    if (Platform.isWindows) {
      final base = Platform.environment['LOCALAPPDATA'];
      dir = Directory(
          '${base == null || base.isEmpty ? Directory.systemTemp.path : base}\\YuhaiVPN');
    } else {
      dir = Directory('${Directory.systemTemp.path}/yuhai-log');
    }
    await dir.create(recursive: true);
    return File('${dir.path}${Platform.isWindows ? '\\' : '/'}client.log');
  }

  static Future<void> record(String message, {File? target}) async {
    try {
      final output = target ?? await ClientLog.file();
      final line = '${DateTime.now().toUtc().toIso8601String()} $message\n';
      await output.writeAsString(line, mode: FileMode.append, flush: true);
    } catch (_) {}
  }

  static Future<void> exception(String where, Object error, StackTrace stack,
      {File? target}) async {
    await record('$where ${error.runtimeType}: $error\n$stack', target: target);
  }
}
