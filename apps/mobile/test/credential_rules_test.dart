import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/features/onboarding/presentation/credential_rules.dart';

void main() {
  test('TRON 地址本地校验与服务端一致', () {
    // USDT 合约地址：公开的已知合法地址（服务端 tron 包测试同一个值）。
    expect(addressProblem('TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t'), isNull);
    expect(addressProblem('  TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t '), isNull);
    expect(addressProblem('TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u'), isNotNull);
    expect(addressProblem('0x0000000000000000000000000000000000000000'), contains('以太坊'));
    expect(addressProblem(''), isNotNull);
    expect(addressProblem('TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6O'), isNotNull);
  });

  test('密码规则', () {
    expect(passwordProblem('abc12345', 'abc12345'), isNull);
    expect(passwordProblem('abc123', 'abc123'), isNotNull);
    expect(passwordProblem('abcdefgh', 'abcdefgh'), isNotNull);
    expect(passwordProblem('12345678', '12345678'), isNotNull);
    expect(passwordProblem('abc12345', 'abc12346'), '两次输入的密码不一样');
  });
}
