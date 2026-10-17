import 'package:crypto/crypto.dart';

const _base58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

/// 本地先查一遍 TRON 地址（T 开头、34 位、Base58Check 校验和），服务端还会再查。
/// 返回 null 表示没问题，否则返回给用户看的提示。
String? addressProblem(String input) {
  final a = input.trim();
  if (a.isEmpty) return '请填写你的 USDT（TRC20）地址';
  if (a.startsWith('0x')) return '这是以太坊格式的地址，请填写 T 开头的 TRC20 地址';
  if (a.length != 34 || !a.startsWith('T')) {
    return 'TRC20 地址以 T 开头，共 34 位';
  }
  var n = BigInt.zero;
  for (final ch in a.split('')) {
    final i = _base58.indexOf(ch);
    if (i < 0) return '地址里有不该出现的字符，请检查';
    n = n * BigInt.from(58) + BigInt.from(i);
  }
  final bytes = <int>[];
  while (n > BigInt.zero) {
    bytes.insert(0, (n & BigInt.from(0xff)).toInt());
    n = n >> 8;
  }
  if (bytes.length != 25 || bytes[0] != 0x41) return '地址不正确，请检查';
  final payload = bytes.sublist(0, 21);
  final check = sha256.convert(sha256.convert(payload).bytes).bytes;
  for (var i = 0; i < 4; i++) {
    if (check[i] != bytes[21 + i]) return '地址校验不通过，可能抄错了一位';
  }
  return null;
}

/// 与服务端规则一致：至少 8 位，同时包含字母和数字。
String? passwordProblem(String password, String again) {
  if (password.length < 8) return '密码至少 8 位';
  if (password.length > 128) return '密码太长了';
  if (!RegExp(r'[A-Za-z]').hasMatch(password) ||
      !RegExp(r'[0-9]').hasMatch(password)) {
    return '密码需要同时包含字母和数字';
  }
  if (password != again) return '两次输入的密码不一样';
  return null;
}
