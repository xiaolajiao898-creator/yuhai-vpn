import 'dart:io';

/// Windows 系统工具的绝对路径。
///
/// 以管理员身份运行时不能靠 PATH / 当前目录去找 net、icacls、sc、powershell：
/// 当前目录或 PATH 前面的同名程序会被以高权限执行。统一从 %SystemRoot%\System32 取，
/// 环境变量缺失或不像一个本地绝对路径时回退到 C:\Windows。
String windowsSystemRoot() {
  final v = Platform.environment['SystemRoot'];
  if (v != null && RegExp(r'^[A-Za-z]:\\[^"*?<>|;]*$').hasMatch(v) && !v.contains('..')) {
    return v.endsWith(r'\') ? v.substring(0, v.length - 1) : v;
  }
  return r'C:\Windows';
}

/// %SystemRoot%\System32\<exe>，例如 net.exe、icacls.exe、sc.exe。
String windowsSystem32Tool(String exe) => '${windowsSystemRoot()}\\System32\\$exe';

/// Windows PowerShell 5.1 的固定位置。
String windowsPowerShellExe() =>
    '${windowsSystemRoot()}\\System32\\WindowsPowerShell\\v1.0\\powershell.exe';
