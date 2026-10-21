import 'dart:async';
import 'dart:io';

import 'package:dio/dio.dart';

/// 发行包没编进 API 地址时会抛 [StateError]。这不是用户的网络故障。
bool isClientConfigError(Object error) =>
    error is StateError || error is ArgumentError || error is AssertionError;

bool isTransportError(Object error) =>
    error is DioException ||
    error is SocketException ||
    error is HttpException ||
    error is HandshakeException ||
    error is TlsException ||
    error is TimeoutException ||
    error is OSError;

String authFailureCopy(Object error) {
  if (isClientConfigError(error)) {
    return '客户端配置有误，暂时无法连接服务器。详情已写入本机日志。';
  }
  if (isTransportError(error)) {
    return '网络不可用，请稍后再试';
  }
  return '出现问题，详情已写入本机日志。';
}
