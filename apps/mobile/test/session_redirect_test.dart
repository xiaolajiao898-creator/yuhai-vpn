import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/router.dart';

void main() {
  test('读取会话失败时停在重试页，而不是欢迎页', () {
    expect(
      sessionRedirect(
        loading: false,
        hasError: true,
        loggedIn: false,
        location: Routes.nodes,
      ),
      Routes.sessionUnavailable,
    );
    expect(
      sessionRedirect(
        loading: false,
        hasError: true,
        loggedIn: false,
        location: Routes.sessionUnavailable,
      ),
      isNull,
    );
  });

  test('确实没有登录时才去欢迎页', () {
    expect(
      sessionRedirect(
        loading: false,
        hasError: false,
        loggedIn: false,
        location: Routes.home,
      ),
      Routes.onboarding,
    );
    expect(
      sessionRedirect(
        loading: true,
        hasError: false,
        loggedIn: false,
        location: Routes.home,
      ),
      Routes.splash,
    );
  });
}
