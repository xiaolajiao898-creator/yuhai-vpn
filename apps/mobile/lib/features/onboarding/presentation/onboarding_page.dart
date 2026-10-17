import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import 'auth_layout.dart';

/// P02 首次引导：品牌标题 + 三条要点 + 注册 / 登录。
class OnboardingPage extends StatelessWidget {
  const OnboardingPage({super.key});

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      body: AuthLayout(
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          mainAxisSize: MainAxisSize.min,
          children: [
            Text('宇海',
                style: text.displayLarge?.copyWith(fontFamily: YhFont.brand)),
            const SizedBox(height: YhSpace.unit),
            Text('专业、稳定的加密网络服务。',
                style: text.titleLarge?.copyWith(color: YhColors.haze)),
            const SizedBox(height: YhSpace.page * 1.5),
            const _Promise('用 USDT 地址注册，不要手机号和邮箱'),
            const _Promise('连接全程加密'),
            const _Promise('不记录你浏览的内容'),
            const SizedBox(height: YhSpace.page * 2),
            FilledButton(
              onPressed: () => context.push(Routes.register),
              child: const Text('注册'),
            ),
            const SizedBox(height: YhSpace.unit * 1.5),
            OutlinedButton(
              onPressed: () => context.push(Routes.login),
              child: const Text('登录'),
            ),
            const SizedBox(height: YhSpace.unit),
            TextButton(
              onPressed: () => context.push(Routes.restore),
              child: const Text('老用户：用账户密钥恢复',
                  style: TextStyle(color: YhColors.haze)),
            ),
          ],
        ),
      ),
    );
  }
}

class _Promise extends StatelessWidget {
  const _Promise(this.text);
  final String text;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: YhSpace.unit),
      child: Row(children: [
        Container(
            width: 6,
            height: 6,
            decoration: const BoxDecoration(
                color: YhColors.glow, shape: BoxShape.circle)),
        const SizedBox(width: YhSpace.unit * 2),
        Expanded(
            child: Text(text, style: Theme.of(context).textTheme.bodyLarge)),
      ]),
    );
  }
}
