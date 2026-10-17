import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:local_auth/local_auth.dart';

/// 移除其他设备前使用系统生物识别或锁屏密码；取消和不支持均不得放行。
class DeviceRemovalAuthenticator {
  DeviceRemovalAuthenticator([LocalAuthentication? auth])
      : _auth = auth ?? LocalAuthentication();
  final LocalAuthentication _auth;

  Future<bool> confirmIdentity() => _auth.authenticate(
        localizedReason: '请验证身份，以移除这台设备',
        options: const AuthenticationOptions(
          biometricOnly: false,
          stickyAuth: false,
          sensitiveTransaction: true,
        ),
      );
}

final deviceRemovalAuthProvider = Provider<DeviceRemovalAuthenticator>(
  (_) => DeviceRemovalAuthenticator(),
);
