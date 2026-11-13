import 'dart:io';

import 'package:crypto/crypto.dart' as crypto;

import 'update_policy.dart';

/// 校验文件。不一致就删除并拒绝安装。
void acceptVerifiedFile(File file, String sha256) {
  final digest = crypto.sha256.convert(file.readAsBytesSync()).toString();
  if (digest != sha256) {
    if (file.existsSync()) file.deleteSync();
    throw const UpdateRejected('安装包校验不一致，已删除');
  }
}

class UpdateRejected implements Exception {
  const UpdateRejected(this.message);
  final String message;
  @override
  String toString() => message;
}

/// 下载安装包到 [directory]，支持断点续传。校验失败就删掉，绝不留下能装的坏文件。
Future<File> downloadRelease({
  required Uri url,
  required String sha256,
  required Directory directory,
  required String fileName,
  bool enforceAllowlist = true,
  void Function(int received, int total)? onProgress,
  HttpClient? client,
}) async {
  if (enforceAllowlist && !releaseUrlAllowed(url)) {
    throw const UpdateRejected('安装包地址不在允许的站点里');
  }
  if (!RegExp(r'^[0-9a-f]{64}$').hasMatch(sha256)) {
    throw const UpdateRejected('发布信息里的校验值无效');
  }
  await directory.create(recursive: true);
  final file = File('${directory.path}${Platform.pathSeparator}$fileName');
  final partial = File('${file.path}.partial');
  var start = 0;
  if (partial.existsSync()) start = partial.lengthSync();
    final http = client ?? HttpClient()
      ..findProxy = (_) => 'DIRECT';
  final ownClient = client == null;
  try {
    final request = await http.getUrl(url);
    if (start > 0) request.headers.set(HttpHeaders.rangeHeader, 'bytes=$start-');
    final response = await request.close();
    final ok = response.statusCode == HttpStatus.ok ||
        (start > 0 && response.statusCode == HttpStatus.partialContent);
    if (!ok) {
      if (partial.existsSync()) partial.deleteSync();
      throw UpdateRejected('下载失败（${response.statusCode}）');
    }
    if (response.statusCode == HttpStatus.ok && start > 0) {
      partial.deleteSync();
      start = 0;
    }
    final sink = partial.openWrite(mode: start > 0 ? FileMode.append : FileMode.write);
    var received = start;
    final totalHeader = response.headers.value(HttpHeaders.contentLengthHeader);
    final total = int.tryParse(totalHeader ?? '') ?? 0;
    final expected = start > 0 && total > 0 ? start + total : total;
    await for (final chunk in response) {
      sink.add(chunk);
      received += chunk.length;
      if (expected > 0) onProgress?.call(received, expected);
    }
    await sink.flush();
    await sink.close();
    acceptVerifiedFile(partial, sha256);
    if (file.existsSync()) file.deleteSync();
    return await partial.rename(file.path);
  } finally {
    if (ownClient) http.close(force: true);
  }
}
