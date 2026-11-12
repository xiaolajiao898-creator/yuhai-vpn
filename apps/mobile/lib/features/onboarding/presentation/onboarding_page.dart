import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

import '../../../app/router.dart';
import '../../../app/theme/tokens.dart';
import '../../../core/widgets/brand.dart';
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
      body: TideBackdrop(
        linesTop: 0.5,
        child: SafeArea(
          child: Column(children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 14, 24, 0),
              child: Row(children: [
                const BrandMark(size: 30),
                const SizedBox(width: 10),
                Text('宇海', style: YhType.display(24)),
                const Spacer(),
                Text('${(_page + 1).toString().padLeft(2, '0')} / 03',
                    style: YhType.latin(13, color: YhColors.sub, weight: FontWeight.w500, spacing: 1.2)),
              ]),
            ),
            Expanded(
              child: PageView(
                controller: _controller,
                onPageChanged: (value) => setState(() => _page = value),
                children: const [
                  _Intro(
                    scene: BrandScene.noContact,
                    kicker: '一步注册',
                    title: '不要手机号，\n也不要邮箱',
                    body: '账号就是你自己钱包里的 USDT（TRC20）地址。密码自己设置，注册时不用转账。',
                  ),
                  _Intro(
                    scene: BrandScene.recover,
                    kicker: '链上找回',
                    title: '忘记密码，\n用地址证明是你',
                    body: '从注册时的那个地址，向 App 显示的官方地址转一笔很小的 TRX。链上确认后就能重设密码。',
                  ),
                  _Intro(
                    scene: BrandScene.connect,
                    kicker: '一键连接',
                    title: '新用户可免费试用\n24 小时',
                    body: '不限流量，最多 3 台设备。到期即停。每个账户和每台设备只能领一次。',
                  ),
                ],
              ),
            ),
            // 底部留 24：电脑窗口没有手机那样的系统底栏留白，16 会让最下面的链接贴着窗口边。
            Padding(
              padding: const EdgeInsets.fromLTRB(24, 0, 24, 24),
              child: Column(children: [
                Row(children: [
                  for (var i = 0; i < 3; i++) ...[
                    if (i > 0) const SizedBox(width: 6),
                    Expanded(
                      child: AnimatedContainer(
                        duration: YhMotion.pageTransition,
                        height: 3,
                        decoration: BoxDecoration(
                          color: i <= _page ? YhColors.accentInk : YhColors.hairStrong,
                          borderRadius: BorderRadius.circular(99),
                        ),
                      ),
                    ),
                  ],
                ]),
                const SizedBox(height: 20),
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
                  const SizedBox(height: 4),
                  TextButton(
                    onPressed: () => context.push(Routes.restore),
                    child: const Text('老用户：用账户密钥恢复'),
                  ),
                ],
              ]),
            ),
          ]),
        ),
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
        padding: const EdgeInsets.fromLTRB(24, 22, 24, 12),
        child: ConstrainedBox(
          constraints: BoxConstraints(minHeight: constraints.maxHeight - 34),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            BrandSceneArt(scene: scene),
            const SizedBox(height: 30),
            Eyebrow(kicker),
            const SizedBox(height: 12),
            Text(title, style: YhType.display(38, height: 1.08)),
            const SizedBox(height: 14),
            Text(body, style: text.bodyLarge?.copyWith(color: YhColors.sub, height: 1.7)),
          ]),
        ),
      );
    });
  }
}
