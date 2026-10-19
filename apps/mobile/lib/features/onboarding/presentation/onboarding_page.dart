import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import 'brand_scenes.dart';

/// 三屏引导：不要手机号邮箱 → 链上找回 → 一键连接。最后一屏才能注册或登录。
class OnboardingPage extends StatefulWidget {
  const OnboardingPage({super.key});

  @override
  State<OnboardingPage> createState() => _OnboardingPageState();
}

class _OnboardingPageState extends State<OnboardingPage> {
  final _controller = PageController();
  var _page = 0;

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final last = _page == 2;
    return Scaffold(
      body: SafeArea(
        child: Column(children: [
          Expanded(
            child: PageView(
              controller: _controller,
              onPageChanged: (value) => setState(() => _page = value),
              children: const [
                _Intro(
                  scene: BrandScene.noContact,
                  kicker: '一步注册',
                  title: '不要手机号，也不要邮箱',
                  body: '账号就是你自己钱包里的 USDT（TRC20）地址。密码自己设置，注册时不用转账。',
                ),
                _Intro(
                  scene: BrandScene.recover,
                  kicker: '链上找回',
                  title: '忘记密码，用地址证明是你',
                  body: '从注册时的那个地址，向 App 显示的官方地址转一笔很小的 TRX。链上确认后就能重设密码。',
                ),
                _Intro(
                  scene: BrandScene.connect,
                  kicker: '一键连接',
                  title: '新用户可免费试用 24 小时',
                  body: '试用是 L1、不限流量、3 台设备，不自动续费。每个账户和每台设备只能领一次。',
                ),
              ],
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(24, 0, 24, 16),
            child: Column(children: [
              Row(mainAxisAlignment: MainAxisAlignment.center, children: [
                for (var i = 0; i < 3; i++)
                  Container(
                    width: i == _page ? 18 : 6,
                    height: 6,
                    margin: const EdgeInsets.symmetric(horizontal: 3),
                    decoration: BoxDecoration(
                      color: i == _page ? YhColors.accent : YhColors.line,
                      borderRadius: BorderRadius.circular(99),
                    ),
                  ),
              ]),
              const SizedBox(height: 16),
              if (!last)
                SizedBox(
                  width: double.infinity,
                  child: FilledButton(
                    onPressed: () => _controller.nextPage(
                        duration: YhMotion.pageTransition, curve: Curves.easeOutCubic),
                    child: const Text('继续'),
                  ),
                )
              else ...[
                SizedBox(
                  width: double.infinity,
                  child: FilledButton(
                    onPressed: () => context.push(Routes.register),
                    child: const Text('注册'),
                  ),
                ),
                const SizedBox(height: 8),
                SizedBox(
                  width: double.infinity,
                  child: OutlinedButton(
                    onPressed: () => context.push(Routes.login),
                    child: const Text('登录'),
                  ),
                ),
                TextButton(
                  onPressed: () => context.push(Routes.restore),
                  child: const Text('老用户：用账户密钥恢复'),
                ),
              ],
            ]),
          ),
        ]),
      ),
    );
  }
}

class _Intro extends StatelessWidget {
  const _Intro({required this.scene, required this.kicker, required this.title, required this.body});
  final BrandScene scene;
  final String kicker, title, body;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return LayoutBuilder(builder: (context, constraints) {
      return SingleChildScrollView(
        padding: const EdgeInsets.fromLTRB(24, 28, 24, 12),
        child: ConstrainedBox(
          constraints: BoxConstraints(minHeight: constraints.maxHeight - 40),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            BrandSceneArt(scene: scene),
            const SizedBox(height: 28),
            Text(kicker, style: text.labelSmall?.copyWith(color: YhColors.accent, letterSpacing: 1.2)),
            const SizedBox(height: 8),
            Text(title, style: text.headlineMedium),
            const SizedBox(height: 12),
            Text(body, style: text.bodyLarge?.copyWith(color: YhColors.sub, height: 1.6)),
          ]),
        ),
      );
    });
  }
}
