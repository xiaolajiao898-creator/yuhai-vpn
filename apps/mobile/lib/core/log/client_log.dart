import 'dart:io';

/// 本机诊断日志。注册和登录的异常写在这里，不进界面，也不带密码。
///
/// Windows 版以管理员身份运行，而日志放在普通用户可写的 %LOCALAPPDATA% 下：
/// - 只有 CI 冒烟（YH_SMOKE_REGISTER=1）才认 YH_CLIENT_LOG 改写路径，平时不能让人用环境变量
///   把管理员进程的写入导到任意文件；
/// - 目录或文件是链接（符号链接、联接点）时不写，免得被引去改别处的文件。
class ClientLog {
  const ClientLog._();

  static Future<File?> file() async {
    final override = Platform.environment['YH_CLIENT_LOG'];
    final smoke = Platform.environment['YH_SMOKE_REGISTER'] == '1';
    if (smoke && override != null && override.trim().isNotEmpty) {
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
    if (await FileSystemEntity.isLink(dir.path)) return null;
    await dir.create(recursive: true);
    if (await FileSystemEntity.isLink(dir.path)) return null;
    final log =
        File('${dir.path}${Platform.isWindows ? '\\' : '/'}client.log');
    if (await FileSystemEntity.isLink(log.path)) return null;
    return log;
  }

  static Future<void> record(String message, {File? target}) async {
    try {
      final output = target ?? await ClientLog.file();
      if (output == null) return;
      final line = '${DateTime.now().toUtc().toIso8601String()} $message\n';
      await output.writeAsString(line, mode: FileMode.append, flush: true);
    } catch (_) {}
  }

  static Future<void> exception(String where, Object error, StackTrace stack,
      {File? target}) async {
    await record('$where ${error.runtimeType}: $error\n$stack', target: target);
  }
}
