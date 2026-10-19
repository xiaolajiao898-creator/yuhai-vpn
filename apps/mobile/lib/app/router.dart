import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../features/home/presentation/home_page.dart';
import '../features/invite/presentation/invite_page.dart';
import '../features/invite/presentation/rewards_page.dart';
import '../features/account/presentation/ledger_page.dart';
import '../features/account/presentation/account_page.dart';
import '../features/account/presentation/redeem_page.dart';
import '../features/account/presentation/devices_page.dart';
import '../features/settings/presentation/settings_page.dart';
import '../features/settings/presentation/connection_settings_page.dart';
import '../features/settings/presentation/about_page.dart';
import '../features/traffic/presentation/traffic_page.dart';
import '../features/onboarding/presentation/backup_page.dart';
import '../features/onboarding/presentation/onboarding_controller.dart';
import '../features/onboarding/presentation/onboarding_page.dart';
import '../features/onboarding/presentation/restore_page.dart';
import '../features/onboarding/presentation/register_page.dart';
import '../features/onboarding/presentation/login_page.dart';
import '../features/onboarding/presentation/reset_password_page.dart';
import '../features/nodes/presentation/nodes_page.dart';
import '../features/plans/presentation/order_confirm_page.dart';
import '../features/plans/presentation/plans_page.dart';
import '../features/payment/data/orders_api.dart';
import '../features/payment/presentation/orders_page.dart';
import '../features/payment/presentation/pay_page.dart';
import '../features/payment/presentation/pay_result_page.dart';
import 'theme/tokens.dart';

/// 路由表与方案 9.4 页面编号一一对应；页面文件在各 feature/presentation 下逐周补齐。
abstract final class Routes {
  static const splash = '/'; // P01
  static const onboarding = '/onboarding'; // P02
  static const backup = '/onboarding/backup'; // P03
  static const restore = '/onboarding/restore'; // P04
  static const register = '/onboarding/register'; // P02a 地址+密码注册
  static const login = '/onboarding/login'; // P02b 地址+密码登录
  static const resetPassword = '/onboarding/reset'; // P02c 转 TRX 找回密码
  static const home = '/home'; // P05
  static const nodes = '/nodes'; // P06
  static const plans = '/plans'; // P07
  static const orderConfirm = '/plans/confirm'; // P08
  static const pay = '/orders/:orderNo/pay'; // P09
  static const payResult = '/orders/:orderNo/result'; // P10
  static const orders = '/orders'; // P11
  static const invite = '/invite'; // P12
  static const rewards = '/invite/rewards'; // P13
  static const settings = '/settings'; // P14
  static const account = '/settings/account'; // P15
  static const redeem = '/settings/redeem';
  static const devices = '/settings/devices'; // P16
  static const connection = '/settings/connection'; // P17
  static const balance = '/settings/balance'; // P18
  static const traffic = '/settings/traffic'; // P19
  static const about = '/settings/about'; // P20
  static const sessionUnavailable = '/session-unavailable';
}

/// Where to send the user given the current session read.
/// A storage error is not a logout: the welcome flow would mint a new device.
String? sessionRedirect({
  required bool loading,
  required bool hasError,
  required bool loggedIn,
  required String location,
}) {
  if (loading) {
    return location == Routes.splash ? null : Routes.splash;
  }
  if (hasError) {
    return location == Routes.sessionUnavailable
        ? null
        : Routes.sessionUnavailable;
  }
  final inOnboarding = location.startsWith(Routes.onboarding);
  if (location == Routes.splash) {
    return loggedIn ? Routes.home : Routes.onboarding;
  }
  if (!loggedIn && !inOnboarding) return Routes.onboarding;
  if (loggedIn && inOnboarding && location != Routes.backup) {
    return Routes.home;
  }
  return null;
}

final routerProvider = Provider<GoRouter>((ref) {
  final notifier = _Notifier(ref);
  ref.onDispose(notifier.dispose);
  final router = GoRouter(
    initialLocation: Routes.splash,
    // 会话变化（登录 / 退出 / 40103 清会话）时重新计算 redirect
    refreshListenable: notifier,
    redirect: (_, state) {
      final session = ref.read(sessionProvider);
      return sessionRedirect(
        loading: session.isLoading,
        hasError: session.hasError,
        loggedIn: session.valueOrNull != null,
        location: state.matchedLocation,
      );
    },
    routes: [
      GoRoute(path: Routes.splash, builder: (_, __) => const YuhaiSplash()),
      GoRoute(
          path: Routes.sessionUnavailable,
          builder: (_, __) => const SessionUnavailablePage()),
      GoRoute(
          path: Routes.onboarding, builder: (_, __) => const OnboardingPage()),
      GoRoute(
          path: Routes.backup,
          builder: (_, s) => BackupPage(args: s.extra as BackupArgs)),
      GoRoute(path: Routes.restore, builder: (_, __) => const RestorePage()),
      GoRoute(path: Routes.register, builder: (_, __) => const RegisterPage()),
      GoRoute(
          path: Routes.login,
          builder: (_, s) => LoginPage(initialAddress: s.extra as String?)),
      GoRoute(
          path: Routes.resetPassword,
          builder: (_, s) =>
              ResetPasswordPage(initialAddress: s.extra as String?)),
      GoRoute(path: Routes.home, builder: (_, __) => const HomePage()),
      GoRoute(path: Routes.nodes, builder: (_, __) => const NodesPage()),
      GoRoute(path: Routes.plans, builder: (_, __) => const PlansPage()),
      GoRoute(
          path: Routes.orderConfirm,
          builder: (_, s) => OrderConfirmPage(planCode: s.extra as String)),
      GoRoute(path: Routes.invite, builder: (_, __) => const InvitePage()),
      GoRoute(path: Routes.rewards, builder: (_, __) => const RewardsPage()),
      GoRoute(path: Routes.balance, builder: (_, __) => const LedgerPage()),
      GoRoute(path: Routes.settings, builder: (_, __) => const SettingsPage()),
      GoRoute(
          path: Routes.connection,
          builder: (_, __) => const ConnectionSettingsPage()),
      GoRoute(path: Routes.traffic, builder: (_, __) => const TrafficPage()),
      GoRoute(path: Routes.about, builder: (_, __) => const AboutPage()),
      GoRoute(path: Routes.account, builder: (_, __) => const AccountPage()),
      GoRoute(path: Routes.redeem, builder: (_, __) => const RedeemPage()),
      GoRoute(path: Routes.devices, builder: (_, __) => const DevicesPage()),
      GoRoute(path: Routes.orders, builder: (_, __) => const OrdersPage()),
      GoRoute(
          path: Routes.pay,
          builder: (_, s) => PayPage(
              orderNo: s.pathParameters['orderNo']!,
              initial: s.extra as Map<String, dynamic>?)),
      GoRoute(
          path: Routes.payResult,
          builder: (_, s) => PayResultPage(
              orderNo: s.pathParameters['orderNo']!,
              order: s.extra is OrderView ? s.extra as OrderView : null)),
    ],
    errorBuilder: (_, state) =>
        Scaffold(body: Center(child: Text('页面不存在：${state.uri}'))),
  );
  ref.onDispose(router.dispose);
  return router;
});

/// P01 启动页：读取本地会话期间显示品牌字标，目标 1.5 秒内离开。
class YuhaiSplash extends StatelessWidget {
  const YuhaiSplash({super.key});
  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      body: Center(
        child: Column(mainAxisSize: MainAxisSize.min, children: [
          Container(
            width: 72,
            height: 72,
            decoration: BoxDecoration(
              color: YhColors.accent,
              borderRadius: BorderRadius.circular(20),
            ),
            child: const CustomPaint(painter: _WaveMark()),
          ),
          const SizedBox(height: 16),
          Text('宇海VPN', style: text.headlineMedium),
        ]),
      ),
    );
  }
}

class _WaveMark extends CustomPainter {
  const _WaveMark();
  @override
  void paint(Canvas canvas, Size size) {
    final paint = Paint()
      ..color = YhColors.buttonInk
      ..style = PaintingStyle.stroke
      ..strokeWidth = 3
      ..strokeCap = StrokeCap.round;
    for (var i = 0; i < 3; i++) {
      final y = size.height * (0.34 + i * 0.16);
      final path = Path()
        ..moveTo(size.width * 0.2, y)
        ..quadraticBezierTo(size.width * 0.35, y - 8, size.width * 0.5, y)
        ..quadraticBezierTo(size.width * 0.65, y + 8, size.width * 0.8, y);
      canvas.drawPath(path, paint);
    }
  }

  @override
  bool shouldRepaint(covariant CustomPainter oldDelegate) => false;
}

class _Notifier extends ChangeNotifier {
  _Notifier(Ref ref) {
    ref.listen(sessionProvider, (_, __) => notifyListeners());
  }
}

/// Shown when secure storage fails. Retry keeps the account; signing in again
/// is an explicit choice and does not happen by itself.
class SessionUnavailablePage extends ConsumerWidget {
  const SessionUnavailablePage({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const Spacer(),
              Text('暂时无法读取登录状态', style: text.headlineMedium),
              const SizedBox(height: 12),
              Text('本机安全存储暂时出错，账户并没有在服务器上退出。请先重试。',
                  style: text.bodyMedium),
              const SizedBox(height: 24),
              FilledButton(
                onPressed: () => ref.invalidate(sessionProvider),
                child: const Text('重试'),
              ),
              const SizedBox(height: 8),
              TextButton(
                onPressed: () =>
                    ref.read(sessionProvider.notifier).signOut(),
                child: const Text('仍然无法读取，重新登录'),
              ),
              const Spacer(),
            ],
          ),
        ),
      ),
    );
  }
}
