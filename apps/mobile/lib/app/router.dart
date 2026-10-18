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
      if (session.isLoading) {
        return state.matchedLocation == Routes.splash ? null : Routes.splash;
      }
      final loggedIn = session.valueOrNull != null;
      final inOnboarding = state.matchedLocation.startsWith(Routes.onboarding);
      if (state.matchedLocation == Routes.splash) {
        return loggedIn ? Routes.home : Routes.onboarding;
      }
      if (!loggedIn && !inOnboarding) return Routes.onboarding;
      if (loggedIn && inOnboarding && state.matchedLocation != Routes.backup) {
        return Routes.home;
      }
      return null;
    },
    routes: [
      GoRoute(path: Routes.splash, builder: (_, __) => const _Splash()),
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
class _Splash extends StatelessWidget {
  const _Splash();
  @override
  Widget build(BuildContext context) => Scaffold(
      body: Center(
          child: Text('宇海', style: Theme.of(context).textTheme.displayLarge)));
}

class _Notifier extends ChangeNotifier {
  _Notifier(Ref ref) {
    ref.listen(sessionProvider, (_, __) => notifyListeners());
  }
}
