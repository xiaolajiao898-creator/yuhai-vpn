import 'dart:io';

/// 服务端 DeviceInfo.platform 的取值：ANDROID / IOS / WINDOWS / MACOS。
String devicePlatformName() {
  if (Platform.isIOS) return 'IOS';
  if (Platform.isWindows) return 'WINDOWS';
  if (Platform.isMacOS) return 'MACOS';
  return 'ANDROID';
}

/// 是否运行在电脑上（Windows / macOS）。
bool get isDesktop => Platform.isWindows || Platform.isMacOS;
