import 'dart:async';
import 'dart:io';

import 'package:dio/dio.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/core/log/client_log.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/auth_failure.dart';

void main() {
  test('配置错误和网络错误使用不同提示', () {
    expect(
      authFailureCopy(StateError('正式构建必须使用有效的 HTTPS API 域名')),
      '客户端配置有误，暂时无法连接服务器。详情已写入本机日志。',
    );
    expect(
      authFailureCopy(ArgumentError('YH_API_BASE')),
      '客户端配置有误，暂时无法连接服务器。详情已写入本机日志。',
    );
    expect(
      authFailureCopy(DioException(
        requestOptions: RequestOptions(path: '/v1/auth/address/register'),
        type: DioExceptionType.connectionError,
        error: const SocketException('Connection refused'),
      )),
      '网络不可用，请稍后再试',
    );
    expect(
      authFailureCopy(const SocketException('failed host lookup')),
      '网络不可用，请稍后再试',
    );
    expect(authFailureCopy(TimeoutException('slow')), '网络不可用，请稍后再试');
    expect(authFailureCopy(StateError('x')), isNot(contains('网络不可用')));
  });

  test('异常写入本地日志，日志里没有口令', () async {
    final file = File('${Directory.systemTemp.path}/yuhai-client-log-test.log');
    if (file.existsSync()) file.deleteSync();
    await ClientLog.exception(
      'register',
      StateError('正式构建必须使用有效的 HTTPS API 域名'),
      StackTrace.empty,
      target: file,
    );
    final text = file.readAsStringSync();
    expect(text, contains('register StateError'));
    expect(text, contains('正式构建必须使用有效的 HTTPS API 域名'));
    expect(text, isNot(contains('password1')));
    file.deleteSync();
  });
}
