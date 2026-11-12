import 'package:flutter/foundation.dart';
import 'package:flutter/services.dart';

/// Android FLAG_SECURE：显示敏感信息（账户密钥）的页面打开期间禁止截屏、录屏，
/// 并在最近任务里不显示页面内容。其它平台无此能力，调用为空操作。
///
/// 采用引用计数：多个敏感页面叠加时，只有最后一个关闭后才解除。
class SecureWindow {
  SecureWindow._();
  static const _ch = MethodChannel('yuhai/window');
  static int _holders = 0;

  static bool get _supported => !kIsWeb && defaultTargetPlatform == TargetPlatform.android;

  static Future<void> acquire() async {
    _holders++;
    if (_holders == 1) await _set(true);
  }

  static Future<void> release() async {
    if (_holders == 0) return;
    _holders--;
    if (_holders == 0) await _set(false);
  }

  static Future<void> _set(bool on) async {
    if (!_supported) return;
    try {
      await _ch.invokeMethod<void>('setSecure', on);
    } catch (_) {
      // 测试环境或旧原生壳没有该通道：不影响页面本身。
    }
  }
}
